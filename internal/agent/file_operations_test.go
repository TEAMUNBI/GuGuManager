package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/runtime"
)

// ---------------------------------------------------------------------------
// fakeFileRuntime：专用于文件操作测试的 containerRuntime 实现
// ---------------------------------------------------------------------------

type fakeFileRuntime struct {
	mu sync.Mutex

	execOutput string
	execErr    error
	execCalls  []fakeExecCall

	copyToErr   error
	copyToCalls []fakeCopyCall

	inspectStatus runtime.ContainerStatus
	inspectErr    error

	startCalls int
	startErr   error

	listRunningResult []string
	listRunningErr    error
}

type fakeExecCall struct {
	Container string
	Argv      []string
}

type fakeCopyCall struct {
	Container     string
	HostPath      string
	ContainerPath string
}

func (f *fakeFileRuntime) CreateContainer(_ context.Context, _ runtime.ContainerConfig) (string, error) {
	return "fake-container-id", nil
}

func (f *fakeFileRuntime) StartContainer(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.startErr
}

func (f *fakeFileRuntime) StopContainer(_ context.Context, _ string, _ int) error {
	return nil
}

func (f *fakeFileRuntime) RestartContainer(_ context.Context, _ string, _ int) error {
	return nil
}

func (f *fakeFileRuntime) RemoveContainer(_ context.Context, _ string, _ bool) error {
	return nil
}

func (f *fakeFileRuntime) InspectContainer(_ context.Context, _ string) (runtime.ContainerStatus, error) {
	return f.inspectStatus, f.inspectErr
}

func (f *fakeFileRuntime) ExecInContainer(_ context.Context, containerID string, argv []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, fakeExecCall{Container: containerID, Argv: argv})
	return f.execOutput, f.execErr
}

func (f *fakeFileRuntime) ContainerStats(_ context.Context, _ string) (runtime.ContainerStats, error) {
	return runtime.ContainerStats{}, nil
}

func (f *fakeFileRuntime) FollowLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (f *fakeFileRuntime) InspectEnv(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeFileRuntime) ListRunningContainers(_ context.Context, _ string) ([]string, error) {
	return f.listRunningResult, f.listRunningErr
}

func (f *fakeFileRuntime) CopyArchiveToContainer(_ context.Context, containerID, hostPath, containerPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copyToCalls = append(f.copyToCalls, fakeCopyCall{
		Container:     containerID,
		HostPath:      hostPath,
		ContainerPath: containerPath,
	})
	return f.copyToErr
}

func (f *fakeFileRuntime) CopyArchiveFromContainer(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *fakeFileRuntime) lastExecCall() (fakeExecCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execCalls) == 0 {
		return fakeExecCall{}, false
	}
	return f.execCalls[len(f.execCalls)-1], true
}

func (f *fakeFileRuntime) lastCopyToCall() (fakeCopyCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.copyToCalls) == 0 {
		return fakeCopyCall{}, false
	}
	return f.copyToCalls[len(f.copyToCalls)-1], true
}

func newExecutorWithFakeRuntime(t *testing.T, rt *fakeFileRuntime) *DockerExecutor {
	t.Helper()
	exec, _ := NewDockerExecutor(t.TempDir())
	exec.rt = rt
	return exec
}

// ===========================================================================
// 1. 纯函数测试
// ===========================================================================

// --- safeContainerPath ---

func TestSafeContainerPath_EmptyPath(t *testing.T) {
	got, err := safeContainerPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data" {
		t.Errorf("safeContainerPath(\"\") = %q, want /data", got)
	}
}

func TestSafeContainerPath_WhitespaceOnly(t *testing.T) {
	got, err := safeContainerPath("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data" {
		t.Errorf("safeContainerPath(\"   \") = %q, want /data", got)
	}
}

func TestSafeContainerPath_RelativePath(t *testing.T) {
	got, err := safeContainerPath("plugins")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/plugins" {
		t.Errorf("safeContainerPath(\"plugins\") = %q, want /data/plugins", got)
	}
}

func TestSafeContainerPath_NestedRelativePath(t *testing.T) {
	got, err := safeContainerPath("plugins/MyPlugin/config.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/plugins/MyPlugin/config.yml" {
		t.Errorf("got %q, want /data/plugins/MyPlugin/config.yml", got)
	}
}

func TestSafeContainerPath_AbsolutePath(t *testing.T) {
	got, err := safeContainerPath("/plugins")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/plugins" {
		t.Errorf("safeContainerPath(\"/plugins\") = %q, want /data/plugins", got)
	}
}

func TestSafeContainerPath_TraversalNeutralized(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"../etc/passwd", "/data/etc/passwd"},
		{"../../etc/passwd", "/data/etc/passwd"},
		{"../../../etc/shadow", "/data/etc/shadow"},
		{"/../../../etc/passwd", "/data/etc/passwd"},
		{"plugins/../../etc/passwd", "/data/etc/passwd"},
		{"..", "/data"},
		{"/..", "/data"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := safeContainerPath(tc.input)
			if err != nil {
				t.Fatalf("safeContainerPath(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("safeContainerPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if !strings.HasPrefix(got, "/data") {
				t.Errorf("safeContainerPath(%q) = %q, escapes /data root", tc.input, got)
			}
		})
	}
}

func TestSafeContainerPath_PathWithSpaces(t *testing.T) {
	got, err := safeContainerPath("my folder/file name.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/my folder/file name.txt" {
		t.Errorf("got %q, want /data/my folder/file name.txt", got)
	}
}

func TestSafeContainerPath_TrimSpaces(t *testing.T) {
	got, err := safeContainerPath("  plugins  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data/plugins" {
		t.Errorf("got %q, want /data/plugins", got)
	}
}

func TestSafeContainerPath_SlashNormalization(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"plugins//subdir", "/data/plugins/subdir"},
		{"./plugins", "/data/plugins"},
		{"plugins/./subdir", "/data/plugins/subdir"},
		{"plugins/subdir/", "/data/plugins/subdir"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := safeContainerPath(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("safeContainerPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSafeContainerPath_DataRootItself(t *testing.T) {
	got, err := safeContainerPath("/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/data" {
		t.Errorf("safeContainerPath(\"/\") = %q, want /data", got)
	}
}

// --- parseLsLine ---

func TestParseLsLine_Directory(t *testing.T) {
	line := "drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 plugins"
	entry, ok := parseLsLine(line, "")
	if !ok {
		t.Fatal("parseLsLine returned ok=false")
	}
	if entry.GetName() != "plugins" {
		t.Errorf("name = %q, want plugins", entry.GetName())
	}
	if !entry.GetDirectory() {
		t.Error("expected directory=true")
	}
	if entry.GetSizeBytes() != 4096 {
		t.Errorf("size = %d, want 4096", entry.GetSizeBytes())
	}
	if entry.GetModifiedAt() == nil {
		t.Error("expected modified_at to be set")
	}
}

func TestParseLsLine_RegularFile(t *testing.T) {
	line := "-rw-r--r-- 1 root root 1234 2024-06-01 08:00:00.000000000 +0000 server.properties"
	entry, ok := parseLsLine(line, "")
	if !ok {
		t.Fatal("parseLsLine returned ok=false")
	}
	if entry.GetName() != "server.properties" {
		t.Errorf("name = %q, want server.properties", entry.GetName())
	}
	if entry.GetDirectory() {
		t.Error("expected directory=false for regular file")
	}
	if entry.GetSizeBytes() != 1234 {
		t.Errorf("size = %d, want 1234", entry.GetSizeBytes())
	}
}

func TestParseLsLine_SymbolicLink(t *testing.T) {
	line := "lrwxrwxrwx 1 root root 20 2024-03-10 12:00:00.000000000 +0000 logs -> /data/logs"
	entry, ok := parseLsLine(line, "")
	if !ok {
		t.Fatal("parseLsLine returned ok=false")
	}
	if entry.GetName() != "logs -> /data/logs" {
		t.Errorf("name = %q, want \"logs -> /data/logs\"", entry.GetName())
	}
	if entry.GetDirectory() {
		t.Error("symlink should not be marked as directory (perm[0] is 'l')")
	}
}

func TestParseLsLine_FilenameWithSpaces(t *testing.T) {
	line := "-rw-r--r-- 1 root root 100 2024-01-15 10:30:00.000000000 +0000 my config file.txt"
	entry, ok := parseLsLine(line, "")
	if !ok {
		t.Fatal("parseLsLine returned ok=false")
	}
	if entry.GetName() != "my config file.txt" {
		t.Errorf("name = %q, want \"my config file.txt\"", entry.GetName())
	}
}

func TestParseLsLine_SpecialPermissions(t *testing.T) {
	cases := []struct {
		name string
		perm string
		dir  bool
	}{
		{"suid_file", "-rwsr-xr-x", false},
		{"sgid_dir", "drwxr-sr-x", true},
		{"sticky_dir", "drwxrwxrwt", true},
		{"char_device", "crw-rw-rw-", false},
		{"block_device", "brw-rw----", false},
		{"pipe", "prw-r--r--", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := fmt.Sprintf("%s 1 root root 0 2024-01-15 10:30:00.000000000 +0000 special", tc.perm)
			entry, ok := parseLsLine(line, "")
			if !ok {
				t.Fatal("parseLsLine returned ok=false")
			}
			if entry.GetDirectory() != tc.dir {
				t.Errorf("directory = %v, want %v (perm=%s)", entry.GetDirectory(), tc.dir, tc.perm)
			}
		})
	}
}

func TestParseLsLine_PathConstruction(t *testing.T) {
	line := "-rw-r--r-- 1 root root 100 2024-01-15 10:30:00.000000000 +0000 config.yml"
	entry, ok := parseLsLine(line, "plugins/MyPlugin")
	if !ok {
		t.Fatal("ok=false")
	}
	if entry.GetPath() != "/plugins/MyPlugin/config.yml" {
		t.Errorf("path = %q, want /plugins/MyPlugin/config.yml", entry.GetPath())
	}
}

func TestParseLsLine_PathConstructionWithLeadingSlash(t *testing.T) {
	line := "-rw-r--r-- 1 root root 100 2024-01-15 10:30:00.000000000 +0000 config.yml"
	entry, ok := parseLsLine(line, "/plugins")
	if !ok {
		t.Fatal("ok=false")
	}
	if entry.GetPath() != "/plugins/config.yml" {
		t.Errorf("path = %q, want /plugins/config.yml", entry.GetPath())
	}
}

func TestParseLsLine_SkipsDotAndDotDot(t *testing.T) {
	for _, name := range []string{".", ".."} {
		line := fmt.Sprintf("drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 %s", name)
		_, ok := parseLsLine(line, "")
		if ok {
			t.Errorf("parseLsLine should skip %q, got ok=true", name)
		}
	}
}

func TestParseLsLine_TooFewFields(t *testing.T) {
	line := "-rw-r--r-- 1 root root 100"
	_, ok := parseLsLine(line, "")
	if ok {
		t.Error("expected ok=false for short line")
	}
}

func TestParseLsLine_TotalLine(t *testing.T) {
	line := "total 48"
	_, ok := parseLsLine(line, "")
	if ok {
		t.Error("expected ok=false for total line")
	}
}

func TestParseLsLine_InvalidSizeDefaultsZero(t *testing.T) {
	line := "-rw-r--r-- 1 root root NOTNUM 2024-01-15 10:30:00.000000000 +0000 file.txt"
	entry, ok := parseLsLine(line, "")
	if !ok {
		t.Fatal("ok=false")
	}
	if entry.GetSizeBytes() != 0 {
		t.Errorf("size = %d, want 0 for non-numeric size", entry.GetSizeBytes())
	}
}

// --- shellQuote ---

func TestShellQuote_PlainString(t *testing.T) {
	got := shellQuote("hello")
	want := "'hello'"
	if got != want {
		t.Errorf("shellQuote(\"hello\") = %q, want %q", got, want)
	}
}

func TestShellQuote_EmptyString(t *testing.T) {
	got := shellQuote("")
	want := "''"
	if got != want {
		t.Errorf("shellQuote(\"\") = %q, want %q", got, want)
	}
}

func TestShellQuote_StringWithSingleQuote(t *testing.T) {
	got := shellQuote("it's")
	want := "'it'\\''s'"
	if got != want {
		t.Errorf("shellQuote(\"it's\") = %q, want %q", got, want)
	}
}

func TestShellQuote_SpecialCharacters(t *testing.T) {
	got := shellQuote("hello; rm -rf / && echo $PATH")
	want := "'hello; rm -rf / && echo $PATH'"
	if got != want {
		t.Errorf("shellQuote(special) = %q, want %q", got, want)
	}
}

func TestShellQuote_PathWithSpaces(t *testing.T) {
	got := shellQuote("/data/my folder/file")
	want := "'/data/my folder/file'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- fileErrorCode ---

func TestFileErrorCode_NotFound(t *testing.T) {
	cases := []error{
		os.ErrNotExist,
		fmt.Errorf("ls: %w", os.ErrNotExist),
		fmt.Errorf("cat: %w", os.ErrNotExist),
	}
	for i, err := range cases {
		got := fileErrorCode(err)
		if got != "NOT_FOUND" {
			t.Errorf("case %d: fileErrorCode(%v) = %q, want NOT_FOUND", i, err, got)
		}
	}
}

func TestFileErrorCode_Forbidden(t *testing.T) {
	cases := []error{
		os.ErrPermission,
		fmt.Errorf("cat: %w", os.ErrPermission),
		fmt.Errorf("mkdir: %w", os.ErrPermission),
	}
	for i, err := range cases {
		got := fileErrorCode(err)
		if got != "FORBIDDEN" {
			t.Errorf("case %d: fileErrorCode(%v) = %q, want FORBIDDEN", i, err, got)
		}
	}
}

func TestFileErrorCode_ValidationFailed(t *testing.T) {
	cases := []error{
		os.ErrInvalid,
		fmt.Errorf("invalid base64: %w", os.ErrInvalid),
	}
	for i, err := range cases {
		got := fileErrorCode(err)
		if got != "VALIDATION_FAILED" {
			t.Errorf("case %d: fileErrorCode(%v) = %q, want VALIDATION_FAILED", i, err, got)
		}
	}
}

func TestFileErrorCode_GenericFailure(t *testing.T) {
	err := errors.New("read-only file system")
	got := fileErrorCode(err)
	if got != "FILE_OPERATION_FAILED" {
		t.Errorf("fileErrorCode(%v) = %q, want FILE_OPERATION_FAILED", err, got)
	}
}

func TestFileErrorCode_NilError(t *testing.T) {
	got := fileErrorCode(nil)
	if got != "FILE_OPERATION_FAILED" {
		t.Errorf("fileErrorCode(nil) = %q, want FILE_OPERATION_FAILED", got)
	}
}

func TestFileErrorCode_DeviceBusy(t *testing.T) {
	err := errors.New("device or resource busy")
	got := fileErrorCode(err)
	if got != "FILE_OPERATION_FAILED" {
		t.Errorf("fileErrorCode(%v) = %q, want FILE_OPERATION_FAILED", err, got)
	}
}

func TestFileErrorCode_NotADirectory(t *testing.T) {
	err := errors.New("not a directory")
	got := fileErrorCode(err)
	if got != "FILE_OPERATION_FAILED" {
		t.Errorf("fileErrorCode(%v) = %q, want FILE_OPERATION_FAILED", err, got)
	}
}

func TestFileErrorCode_IsADirectory(t *testing.T) {
	err := fmt.Errorf("%w: is a directory", os.ErrInvalid)
	got := fileErrorCode(err)
	if got != "VALIDATION_FAILED" {
		t.Errorf("fileErrorCode(%v) = %q, want VALIDATION_FAILED", err, got)
	}
}

// ===========================================================================
// 2. ExecuteFileOperation 集成测试（使用 fakeFileRuntime）
// ===========================================================================

func TestExecuteFileOperation_ListSuccess(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "total 20\ndrwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 .\ndrwxr-xr-x 1 root root 4096 2024-01-15 10:30:00.000000000 +0000 ..\n-rw-r--r-- 1 root root 100 2024-01-15 10:30:00.000000000 +0000 server.properties\ndrwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 plugins\n",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_List{List: &agentv1.FileOperationRequest_ListFilesInput{Path: ""}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	listResult := resp.GetList()
	if listResult == nil {
		t.Fatal("expected list result, got nil")
	}
	entries := listResult.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (. and .. skipped), got %d", len(entries))
	}
	if entries[0].GetName() != "server.properties" {
		t.Errorf("entry[0].name = %q, want server.properties", entries[0].GetName())
	}
	if entries[1].GetName() != "plugins" || !entries[1].GetDirectory() {
		t.Errorf("entry[1] should be plugins directory, got name=%q dir=%v", entries[1].GetName(), entries[1].GetDirectory())
	}

	call, ok := rt.lastExecCall()
	if !ok {
		t.Fatal("expected ExecInContainer to be called")
	}
	if call.Container != "gugu-server-srv1" {
		t.Errorf("container = %q, want gugu-server-srv1", call.Container)
	}
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "ls -la") {
		t.Errorf("command should contain 'ls -la', got: %s", cmd)
	}
	if !strings.Contains(cmd, "/data") {
		t.Errorf("command should reference /data, got: %s", cmd)
	}
}

func TestExecuteFileOperation_ReadSuccess(t *testing.T) {
	content := "hello world\n"
	rt := &fakeFileRuntime{
		execOutput: content + "__EXIT__0",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "test.txt"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	readResult := resp.GetRead()
	if readResult == nil {
		t.Fatal("expected read result, got nil")
	}
	if string(readResult.GetContent()) != content {
		t.Errorf("content = %q, want %q", readResult.GetContent(), content)
	}
	if readResult.GetBase64() {
		t.Error("text file should not be base64")
	}
	if readResult.GetSizeBytes() != uint64(len(content)) {
		t.Errorf("size = %d, want %d", readResult.GetSizeBytes(), len(content))
	}
}

func TestExecuteFileOperation_ReadBinaryBase64(t *testing.T) {
	content := "binary\x00data"
	rt := &fakeFileRuntime{
		execOutput: content + "__EXIT__0",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "binary.dat"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	readResult := resp.GetRead()
	if !readResult.GetBase64() {
		t.Error("binary file should be base64 encoded")
	}
}

func TestExecuteFileOperation_WriteSuccess(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Write{Write: &agentv1.FileOperationRequest_WriteFileInput{
			Path:    "config/new.yml",
			Content: []byte("key: value\n"),
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	if resp.GetWrite() == nil {
		t.Fatal("expected write result, got nil")
	}

	copyCall, ok := rt.lastCopyToCall()
	if !ok {
		t.Fatal("expected CopyArchiveToContainer to be called")
	}
	if copyCall.Container != "gugu-server-srv1" {
		t.Errorf("container = %q, want gugu-server-srv1", copyCall.Container)
	}
	if copyCall.ContainerPath != "/data/config" {
		t.Errorf("containerPath = %q, want /data/config", copyCall.ContainerPath)
	}
	if _, err := os.Stat(copyCall.HostPath); err == nil {
		t.Error("temp tar should have been cleaned up after write")
	}
}

func TestExecuteFileOperation_WriteBase64Success(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	encoded := "AAEC/w=="

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Write{Write: &agentv1.FileOperationRequest_WriteFileInput{
			Path:    "data.bin",
			Content: []byte(encoded),
			Base64:  true,
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_WriteInvalidBase64(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Write{Write: &agentv1.FileOperationRequest_WriteFileInput{
			Path:    "data.bin",
			Content: []byte("!!!not-base64!!!"),
			Base64:  true,
		}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure for invalid base64")
	}
	if resp.GetErrorCode() != "VALIDATION_FAILED" {
		t.Errorf("error_code = %q, want VALIDATION_FAILED", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_MkdirSuccess(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Mkdir{Mkdir: &agentv1.FileOperationRequest_MakeDirectoryInput{Path: "plugins/NewPlugin"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	if resp.GetMkdir() == nil {
		t.Fatal("expected mkdir result, got nil")
	}

	call, ok := rt.lastExecCall()
	if !ok {
		t.Fatal("expected ExecInContainer to be called")
	}
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "mkdir -p") {
		t.Errorf("command should contain 'mkdir -p', got: %s", cmd)
	}
	if !strings.Contains(cmd, "/data/plugins/NewPlugin") {
		t.Errorf("command should reference target path, got: %s", cmd)
	}
}

func TestExecuteFileOperation_MoveSuccess(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Move{Move: &agentv1.FileOperationRequest_MoveFileInput{
			Source:      "old.txt",
			Destination: "new.txt",
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	if resp.GetMove() == nil {
		t.Fatal("expected move result, got nil")
	}

	call, ok := rt.lastExecCall()
	if !ok {
		t.Fatal("expected ExecInContainer to be called")
	}
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "mv -n") {
		t.Errorf("without replace, should use 'mv -n', got: %s", cmd)
	}
}

func TestExecuteFileOperation_MoveWithReplace(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Move{Move: &agentv1.FileOperationRequest_MoveFileInput{
			Source:      "old.txt",
			Destination: "new.txt",
			Replace:     true,
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}

	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "mv -f") {
		t.Errorf("with replace=true, should use 'mv -f', got: %s", cmd)
	}
}

func TestExecuteFileOperation_RemoveSuccess(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Remove{Remove: &agentv1.FileOperationRequest_RemoveFileInput{Path: "old.txt"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	if resp.GetRemove() == nil {
		t.Fatal("expected remove result, got nil")
	}

	call, ok := rt.lastExecCall()
	if !ok {
		t.Fatal("expected ExecInContainer to be called")
	}
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "rm -f") {
		t.Errorf("without recursive, should use 'rm -f', got: %s", cmd)
	}
}

func TestExecuteFileOperation_RemoveRecursive(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Remove{Remove: &agentv1.FileOperationRequest_RemoveFileInput{Path: "olddir", Recursive: true}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}

	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if !strings.Contains(cmd, "rm -rf") {
		t.Errorf("with recursive=true, should use 'rm -rf', got: %s", cmd)
	}
}

func TestExecuteFileOperation_UnknownOperation(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure for nil operation")
	}
	if resp.GetErrorCode() != "VALIDATION_FAILED" {
		t.Errorf("error_code = %q, want VALIDATION_FAILED", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_DownloadBackupSuccess(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)
	backupDir := filepath.Join(exec.dataRoot, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte("gzip-archive-content")
	if err := os.WriteFile(filepath.Join(backupDir, "bkp-0001.tar.gz"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_DownloadBackup{
			DownloadBackup: &agentv1.FileOperationRequest_DownloadBackupInput{BackupId: "bkp-0001"},
		},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got error_code=%s", resp.GetErrorCode())
	}
	result := resp.GetDownloadBackup()
	if result == nil {
		t.Fatal("expected download backup result, got nil")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(result.GetContent()))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Errorf("decoded content = %q, want %q", decoded, raw)
	}
	if !result.GetBase64() {
		t.Error("backup archive should be base64 encoded")
	}
	if result.GetSizeBytes() != uint64(len(raw)) {
		t.Errorf("size = %d, want %d", result.GetSizeBytes(), len(raw))
	}
	if result.GetFilename() != "bkp-0001.tar.gz" {
		t.Errorf("filename = %q, want bkp-0001.tar.gz", result.GetFilename())
	}
	// 下载备份不涉及容器：不应产生 exec / start / copy 调用。
	if len(rt.execCalls) != 0 || rt.startCalls != 0 || len(rt.copyToCalls) != 0 {
		t.Errorf("download touched the container: exec=%v start=%d copy=%v", rt.execCalls, rt.startCalls, rt.copyToCalls)
	}
}

func TestExecuteFileOperation_DownloadBackupNotFound(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_DownloadBackup{DownloadBackup: &agentv1.FileOperationRequest_DownloadBackupInput{BackupId: "missing-0001"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure for missing archive")
	}
	if resp.GetErrorCode() != "NOT_FOUND" {
		t.Errorf("error_code = %q, want NOT_FOUND", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_DownloadBackupEmptyID(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_DownloadBackup{DownloadBackup: &agentv1.FileOperationRequest_DownloadBackupInput{}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure for empty backup id")
	}
	if resp.GetErrorCode() != "VALIDATION_FAILED" {
		t.Errorf("error_code = %q, want VALIDATION_FAILED", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_RuntimeUnavailable(t *testing.T) {
	exec, _ := NewDockerExecutor(t.TempDir())
	exec.newRuntime = func() (containerRuntime, error) {
		return nil, errors.New("docker daemon not reachable")
	}

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_List{List: &agentv1.FileOperationRequest_ListFilesInput{Path: ""}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure when runtime is unavailable")
	}
	if resp.GetErrorCode() != "RUNTIME_UNAVAILABLE" {
		t.Errorf("error_code = %q, want RUNTIME_UNAVAILABLE", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_ListNotFound(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "ls: cannot access '/data/nope': No such file or directory\n",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_List{List: &agentv1.FileOperationRequest_ListFilesInput{Path: "nope"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure")
	}
	if resp.GetErrorCode() != "NOT_FOUND" {
		t.Errorf("error_code = %q, want NOT_FOUND", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_ListPermissionDenied(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "ls: cannot open directory '/data/secret': Permission denied\n",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_List{List: &agentv1.FileOperationRequest_ListFilesInput{Path: "secret"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure")
	}
	if resp.GetErrorCode() != "FORBIDDEN" {
		t.Errorf("error_code = %q, want FORBIDDEN", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_ReadNotFound(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "cat: /data/missing: No such file or directory\n__EXIT__1",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "missing"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure")
	}
	if resp.GetErrorCode() != "NOT_FOUND" {
		t.Errorf("error_code = %q, want NOT_FOUND", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_ReadDirectory(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "cat: /data/plugins: is a directory\n__EXIT__1",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "plugins"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure when reading a directory")
	}
	if resp.GetErrorCode() != "VALIDATION_FAILED" {
		t.Errorf("error_code = %q, want VALIDATION_FAILED", resp.GetErrorCode())
	}
}

func TestExecuteFileOperation_ReadDirectoryCaseMismatch(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "cat: /data/plugins: Is a directory\n__EXIT__1",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "plugins"}},
	})

	if resp.GetSucceeded() {
		t.Fatal("expected failure when reading a directory")
	}
	if resp.GetErrorCode() != "FILE_OPERATION_FAILED" {
		t.Errorf("error_code = %q, want FILE_OPERATION_FAILED (case-sensitive match)", resp.GetErrorCode())
	}
}

// ===========================================================================
// 3. 路径安全测试（通过操作入口验证 safeContainerPath 集成）
// ===========================================================================

func TestPathSecurity_ListTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "total 0\ndrwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 .\n",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_List{List: &agentv1.FileOperationRequest_ListFilesInput{Path: "../../../etc"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	call, ok := rt.lastExecCall()
	if !ok {
		t.Fatal("expected ExecInContainer to be called")
	}
	cmd := strings.Join(call.Argv, " ")
	if strings.Contains(cmd, "'/etc") {
		t.Errorf("command should not access /etc outside /data: %s", cmd)
	}
	if !strings.Contains(cmd, "'/data") {
		t.Errorf("command should be contained within /data: %s", cmd)
	}
}

func TestPathSecurity_ReadTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "safe-content__EXIT__0",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "../etc/passwd"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if strings.Contains(cmd, "'/etc/passwd") {
		t.Errorf("command should not access /etc/passwd outside /data: %s", cmd)
	}
}

func TestPathSecurity_WriteTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Write{Write: &agentv1.FileOperationRequest_WriteFileInput{
			Path:    "../../etc/evil",
			Content: []byte("pwned"),
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	copyCall, ok := rt.lastCopyToCall()
	if !ok {
		t.Fatal("expected CopyArchiveToContainer to be called")
	}
	if !strings.HasPrefix(copyCall.ContainerPath, "/data") {
		t.Errorf("container path should be within /data, got %s", copyCall.ContainerPath)
	}
}

func TestPathSecurity_MkdirTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Mkdir{Mkdir: &agentv1.FileOperationRequest_MakeDirectoryInput{Path: "../../tmp/evil"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if strings.Contains(cmd, "'/tmp") {
		t.Errorf("command should not access /tmp outside /data: %s", cmd)
	}
}

func TestPathSecurity_MoveTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId: "srv1",
		Operation: &agentv1.FileOperationRequest_Move{Move: &agentv1.FileOperationRequest_MoveFileInput{
			Source:      "../../etc/passwd",
			Destination: "harmless",
		}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if strings.Contains(cmd, "'/etc/passwd") {
		t.Errorf("command should not access /etc/passwd outside /data: %s", cmd)
	}
}

func TestPathSecurity_RemoveTraversalNeutralized(t *testing.T) {
	rt := &fakeFileRuntime{}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "srv1",
		Operation: &agentv1.FileOperationRequest_Remove{Remove: &agentv1.FileOperationRequest_RemoveFileInput{Path: "../../etc"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success (traversal neutralized), got error_code=%s", resp.GetErrorCode())
	}
	call, _ := rt.lastExecCall()
	cmd := strings.Join(call.Argv, " ")
	if strings.Contains(cmd, "'/etc") {
		t.Errorf("command should not access /etc outside /data: %s", cmd)
	}
}

func TestPathSecurity_ContainerNameIsolated(t *testing.T) {
	rt := &fakeFileRuntime{
		execOutput: "__EXIT__0",
	}
	exec := newExecutorWithFakeRuntime(t, rt)

	resp := exec.ExecuteFileOperation(context.Background(), &agentv1.FileOperationRequest{
		ServerId:  "my-server",
		Operation: &agentv1.FileOperationRequest_Read{Read: &agentv1.FileOperationRequest_ReadFileInput{Path: "file.txt"}},
	})

	if !resp.GetSucceeded() {
		t.Fatalf("expected success, got %s", resp.GetErrorCode())
	}
	call, _ := rt.lastExecCall()
	if call.Container != "gugu-server-my-server" {
		t.Errorf("container name = %q, want gugu-server-my-server", call.Container)
	}
}

func TestPathSecurity_AllOperationsRestrictedToDataRoot(t *testing.T) {
	passes := []string{"file.txt", "plugins/core", "/config.yml", "subdir/../file.txt", "a/b/c/../../d"}
	for _, p := range passes {
		got, err := safeContainerPath(p)
		if err != nil {
			t.Errorf("safeContainerPath(%q) unexpected error: %v", p, err)
			continue
		}
		if !strings.HasPrefix(got, "/data/") && got != "/data" {
			t.Errorf("safeContainerPath(%q) = %q, does not stay under /data", p, got)
		}
	}
}
