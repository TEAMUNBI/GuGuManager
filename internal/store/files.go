package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gugumanager/gugumanager/internal/domain"
	serverfiles "github.com/gugumanager/gugumanager/internal/files"
	"github.com/gugumanager/gugumanager/internal/id"
)

const (
	developmentMaxReadBytes  = 8 * 1024 * 1024
	developmentMaxWriteBytes = 8 * 1024 * 1024
)

func (m *Memory) initializeFileSystems() error {
	if strings.TrimSpace(m.fileRoot) == "" {
		return fmt.Errorf("file root must not be empty")
	}
	if err := os.MkdirAll(m.fileRoot, 0o750); err != nil {
		return fmt.Errorf("create file root: %w", err)
	}
	rootInfo, err := os.Lstat(m.fileRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("file root must be a real directory")
	}
	for _, serverID := range m.serverOrder {
		root := filepath.Join(m.fileRoot, serverID)
		if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(root, 0o750); err != nil {
				return fmt.Errorf("create server file root: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("inspect server file root: %w", err)
		}
		filesystem, err := serverfiles.NewServerFS(root, serverfiles.Limits{MaxReadBytes: developmentMaxReadBytes, MaxWriteBytes: developmentMaxWriteBytes})
		if err != nil {
			return fmt.Errorf("initialize server file root %s: %w", serverID, err)
		}
		for _, entry := range m.files[serverID] {
			if err := seedFileEntry(filesystem, entry); err != nil {
				return fmt.Errorf("seed server file %s: %w", entry.Path, err)
			}
		}
		m.fileSystems[serverID] = filesystem
	}
	return nil
}

// createServerFileSystemLocked is called before a new in-memory server is
// committed. The caller holds m.mu, so a filesystem failure can abort without
// leaving a server, allocation, or resource reservation behind.
func (m *Memory) createServerFileSystemLocked(serverID string) error {
	root := filepath.Join(m.fileRoot, serverID)
	if err := os.Mkdir(root, 0o750); err != nil {
		return err
	}
	filesystem, err := serverfiles.NewServerFS(root, serverfiles.Limits{MaxReadBytes: developmentMaxReadBytes, MaxWriteBytes: developmentMaxWriteBytes})
	if err != nil {
		_ = os.Remove(root)
		return err
	}
	m.fileSystems[serverID] = filesystem
	m.files[serverID] = []domain.FileEntry{}
	return nil
}

func seedFileEntry(filesystem *serverfiles.ServerFS, entry domain.FileEntry) error {
	normalized, err := serverfiles.NormalizeRelative(entry.Path)
	if err != nil || normalized == "" {
		return fmt.Errorf("invalid seed path %q", entry.Path)
	}
	if existing, statErr := filesystem.Stat(normalized); statErr == nil {
		if entry.Kind == "directory" && !existing.Directory || entry.Kind != "directory" && existing.Directory {
			return fmt.Errorf("seed target %q has an unexpected type", entry.Path)
		}
		return nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	if entry.Kind == "directory" {
		return filesystem.Mkdir(normalized)
	}
	content := []byte("# development fixture: " + normalized + "\n")
	switch strings.ToLower(filepath.Base(normalized)) {
	case "eula.txt":
		content = []byte("eula=true\n")
	case "server.properties":
		content = []byte("motd=GuGuManager development\n")
	case "level.dat":
		content = []byte("development-world-state\n")
	}
	return filesystem.WriteFile(normalized, content)
}

func (m *Memory) serverFileSystem(serverID string) (*serverfiles.ServerFS, error) {
	m.mu.RLock()
	filesystem, ok := m.fileSystems[serverID]
	m.mu.RUnlock()
	if !ok || filesystem == nil {
		return nil, domain.NewProblem("NOT_FOUND", "服务器文件目录不存在", false)
	}
	return filesystem, nil
}

func (m *Memory) fileMutationGate(serverID string) *sync.RWMutex {
	gate, _ := m.fileMutationGates.LoadOrStore(serverID, &sync.RWMutex{})
	return gate.(*sync.RWMutex)
}

func (m *Memory) beginFileMutation(serverID string, actorID string) (domain.Server, *serverfiles.ServerFS, domain.User, func(), error) {
	gate := m.fileMutationGate(serverID)
	// Keep both the per-server mutation gate and the Store lock held for the
	// complete physical operation. This makes user disable/demotion and
	// membership revocation mutually exclusive with the filesystem side effect.
	gate.Lock()

	m.mu.Lock()
	actor, err := m.authorizeServerLocked(actorID, serverID, "servers.files.write")
	if err != nil {
		m.mu.Unlock()
		gate.Unlock()
		return domain.Server{}, nil, domain.User{}, nil, err
	}
	m.reconcileNodeLivenessLocked(time.Now().UTC())
	server, ok := m.servers[serverID]
	if !ok {
		m.mu.Unlock()
		gate.Unlock()
		return domain.Server{}, nil, domain.User{}, nil, domain.NewProblem("NOT_FOUND", "服务器不存在", false)
	}
	if operation, ok := m.activeRestoreOperationLocked(serverID); ok {
		m.mu.Unlock()
		gate.Unlock()
		return domain.Server{}, nil, domain.User{}, nil, operationInProgress(operation)
	}
	filesystem, ok := m.fileSystems[serverID]
	if !ok || filesystem == nil {
		m.mu.Unlock()
		gate.Unlock()
		return domain.Server{}, nil, domain.User{}, nil, domain.NewProblem("NOT_FOUND", "服务器文件目录不存在", false)
	}
	if m.fileMutationHook != nil {
		m.fileMutationHook()
	}
	release := func() {
		m.mu.Unlock()
		gate.Unlock()
	}
	return server, filesystem, actor, release, nil
}

func (m *Memory) activeRestoreOperationLocked(serverID string) (domain.Operation, bool) {
	for _, operation := range m.operations {
		if operation.ServerID == serverID && operation.Type == domain.PowerAction("restore") && !isTerminalOperation(operation.Status) {
			return operation, true
		}
	}
	return domain.Operation{}, false
}

func (m *Memory) ReadFile(serverID string, requestedPath string) (domain.FileContent, error) {
	if _, err := m.Server(serverID); err != nil {
		return domain.FileContent{}, err
	}
	filesystem, err := m.serverFileSystem(serverID)
	if err != nil {
		return domain.FileContent{}, err
	}
	content, err := filesystem.ReadFile(requestedPath)
	if err != nil {
		return domain.FileContent{}, mapFileError(err)
	}
	entry, err := filesystem.Stat(requestedPath)
	if err != nil {
		return domain.FileContent{}, mapFileError(err)
	}
	encoded := string(content)
	encoding := "utf-8"
	if !utf8.Valid(content) {
		encoded = base64.RawStdEncoding.EncodeToString(content)
		encoding = "base64"
	}
	return domain.FileContent{Path: entry.Path, Content: encoded, Encoding: encoding, SizeBytes: entry.SizeBytes, ModifiedAt: entry.ModifiedAt}, nil
}

func (m *Memory) WriteFile(serverID string, requestedPath string, content []byte, actor domain.User) error {
	server, filesystem, currentActor, release, err := m.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	if err := filesystem.WriteFile(requestedPath, content); err != nil {
		release()
		return mapFileError(err)
	}
	release()
	m.recordAudit(currentActor.DisplayName, "file.write", "server", server.Name, "success", id.New())
	return nil
}

func (m *Memory) CreateDirectory(serverID string, requestedPath string, actor domain.User) error {
	server, filesystem, currentActor, release, err := m.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	if err := filesystem.Mkdir(requestedPath); err != nil {
		release()
		return mapFileError(err)
	}
	release()
	m.recordAudit(currentActor.DisplayName, "file.mkdir", "server", server.Name, "success", id.New())
	return nil
}

func (m *Memory) MoveFile(serverID string, source string, destination string, replace bool, actor domain.User) error {
	server, filesystem, currentActor, release, err := m.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	if err := filesystem.Move(source, destination, replace); err != nil {
		release()
		return mapFileError(err)
	}
	release()
	m.recordAudit(currentActor.DisplayName, "file.move", "server", server.Name, "success", id.New())
	return nil
}

func (m *Memory) DeleteFile(serverID string, requestedPath string, recursive bool, actor domain.User) error {
	server, filesystem, currentActor, release, err := m.beginFileMutation(serverID, actor.ID)
	if err != nil {
		return err
	}
	if err := filesystem.Delete(requestedPath, recursive); err != nil {
		release()
		return mapFileError(err)
	}
	release()
	m.recordAudit(currentActor.DisplayName, "file.delete", "server", server.Name, "success", id.New())
	return nil
}

func mapFileError(err error) error {
	switch {
	case errors.Is(err, serverfiles.ErrPathEscape), errors.Is(err, serverfiles.ErrUnsafePath), errors.Is(err, serverfiles.ErrRootMutation), errors.Is(err, serverfiles.ErrUnsupportedFileType):
		return domain.NewProblem("PATH_ESCAPE_BLOCKED", "文件路径或类型不安全", false)
	case errors.Is(err, serverfiles.ErrSizeLimit):
		return domain.NewProblem("VALIDATION_FAILED", "文件大小超过服务器文件操作限制", false)
	case errors.Is(err, serverfiles.ErrPathLimit):
		return domain.NewProblem("VALIDATION_FAILED", "文件路径超过允许长度", false)
	case errors.Is(err, fs.ErrNotExist):
		return domain.NewProblem("NOT_FOUND", "文件或目录不存在", false)
	case errors.Is(err, fs.ErrExist):
		return domain.NewProblem("OPERATION_CONFLICT", "目标文件或目录已经存在", false)
	case errors.Is(err, serverfiles.ErrNotDirectory), errors.Is(err, serverfiles.ErrNotRegularFile), errors.Is(err, serverfiles.ErrInvalidMove):
		return domain.NewProblem("VALIDATION_FAILED", "文件操作目标类型不匹配", false)
	default:
		return domain.NewProblem("INTERNAL_ERROR", "文件操作失败", true)
	}
}
