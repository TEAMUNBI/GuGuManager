package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnsafePath          = errors.New("path contains an unsafe filesystem entry")
	ErrNotDirectory        = errors.New("path is not a directory")
	ErrNotRegularFile      = errors.New("path is not a regular file")
	ErrUnsupportedFileType = errors.New("unsupported filesystem entry type")
	ErrSizeLimit           = errors.New("file size limit exceeded")
	ErrRootMutation        = errors.New("server root cannot be modified")
	ErrInvalidMove         = errors.New("invalid move")
)

type Limits struct {
	MaxReadBytes  int64
	MaxWriteBytes int64
}

// Entry is a validated child of a server data directory. The package keeps
// the shape independent from the HTTP/domain packages so it can be reused by
// an Agent without introducing an import cycle.
type Entry struct {
	Name       string
	Path       string
	Directory  bool
	SizeBytes  int64
	ModifiedAt time.Time
}

type ServerFS struct {
	root     string
	rootReal string
	rootInfo fs.FileInfo
	limits   Limits
	mu       sync.Mutex
}

// Limits reports the ceilings this filesystem enforces. Callers that stream
// large content check this first so an over-long write fails before the bytes
// are fetched rather than after.
func (s *ServerFS) Limits() Limits {
	return s.limits
}

type resolvedEntry struct {
	clean  string
	full   string
	info   fs.FileInfo
	exists bool
}

type treeEntry struct {
	full string
	info fs.FileInfo
}

func NewServerFS(root string, limits Limits) (*ServerFS, error) {
	if limits.MaxReadBytes <= 0 || limits.MaxWriteBytes <= 0 {
		return nil, fmt.Errorf("limits: %w", fs.ErrInvalid)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("server root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)
	info, err := os.Lstat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("server root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("server root: %w", ErrUnsafePath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("server root: %w", ErrNotDirectory)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("server root: %w", err)
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return nil, fmt.Errorf("server root: %w", err)
	}
	return &ServerFS{
		root:     absRoot,
		rootReal: filepath.Clean(realRoot),
		rootInfo: info,
		limits:   limits,
	}, nil
}

func (s *ServerFS) ReadFile(requested string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, false)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", requested, err)
	}
	if !entry.info.Mode().IsRegular() {
		return nil, fmt.Errorf("read %q: %w", requested, ErrNotRegularFile)
	}
	if entry.info.Size() > s.limits.MaxReadBytes {
		return nil, fmt.Errorf("read %q: %w", requested, ErrSizeLimit)
	}

	file, err := os.Open(entry.full)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", requested, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", requested, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("read %q: %w", requested, ErrNotRegularFile)
	}
	if !os.SameFile(entry.info, openedInfo) {
		return nil, fmt.Errorf("read %q: file changed during resolution: %w", requested, ErrUnsafePath)
	}

	content, err := io.ReadAll(io.LimitReader(file, s.limits.MaxReadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", requested, err)
	}
	if int64(len(content)) > s.limits.MaxReadBytes {
		return nil, fmt.Errorf("read %q: %w", requested, ErrSizeLimit)
	}
	return content, nil
}

// List returns immediate children only. Every child is resolved and checked
// with Lstat, so a symlink or unsupported device entry cannot be exposed in a
// directory response.
func (s *ServerFS) List(requested string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, false)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", requested, err)
	}
	if !entry.info.IsDir() {
		return nil, fmt.Errorf("list %q: %w", requested, ErrNotDirectory)
	}
	directory, err := os.Open(entry.full)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", requested, err)
	}
	children, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", requested, err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("list %q: %w", requested, closeErr)
	}

	clean := entry.clean
	result := make([]Entry, 0, len(children))
	for _, child := range children {
		childPath := child.Name()
		if clean != "" {
			childPath = path.Join(clean, child.Name())
		}
		resolved, err := s.resolve(childPath, false)
		if err != nil {
			return nil, fmt.Errorf("list %q: child %q: %w", requested, child.Name(), err)
		}
		result = append(result, Entry{
			Name:       resolved.info.Name(),
			Path:       resolved.clean,
			Directory:  resolved.info.IsDir(),
			SizeBytes:  resolved.info.Size(),
			ModifiedAt: resolved.info.ModTime().UTC(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Directory != result[j].Directory {
			return result[i].Directory
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (s *ServerFS) Stat(requested string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, false)
	if err != nil {
		return Entry{}, fmt.Errorf("stat %q: %w", requested, err)
	}
	return Entry{
		Name:       entry.info.Name(),
		Path:       entry.clean,
		Directory:  entry.info.IsDir(),
		SizeBytes:  entry.info.Size(),
		ModifiedAt: entry.info.ModTime().UTC(),
	}, nil
}

func (s *ServerFS) WriteFile(requested string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, true)
	if err != nil {
		return fmt.Errorf("write %q: %w", requested, err)
	}
	if entry.clean == "" {
		return fmt.Errorf("write %q: %w", requested, ErrRootMutation)
	}
	if int64(len(content)) > s.limits.MaxWriteBytes {
		return fmt.Errorf("write %q: %w", requested, ErrSizeLimit)
	}
	if entry.exists && !entry.info.Mode().IsRegular() {
		return fmt.Errorf("write %q: %w", requested, ErrNotRegularFile)
	}

	parent, err := s.resolve(portableParent(entry.clean), false)
	if err != nil {
		return fmt.Errorf("write %q: %w", requested, err)
	}
	if !parent.info.IsDir() {
		return fmt.Errorf("write %q: %w", requested, ErrNotDirectory)
	}

	mode := fs.FileMode(0o640)
	if entry.exists {
		mode = entry.info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(parent.full, ".gugu-write-*")
	if err != nil {
		return fmt.Errorf("write %q: create temporary file: %w", requested, err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("write %q: set temporary file mode: %w", requested, err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write %q: write temporary file: %w", requested, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("write %q: sync temporary file: %w", requested, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write %q: close temporary file: %w", requested, err)
	}

	freshParent, err := s.resolve(parent.clean, false)
	if err != nil || !os.SameFile(parent.info, freshParent.info) {
		return fmt.Errorf("write %q: parent changed during write: %w", requested, ErrUnsafePath)
	}
	freshEntry, err := s.resolve(entry.clean, true)
	if err != nil {
		return fmt.Errorf("write %q: %w", requested, err)
	}
	if entry.exists != freshEntry.exists || entry.exists && !os.SameFile(entry.info, freshEntry.info) {
		return fmt.Errorf("write %q: destination changed during write: %w", requested, ErrUnsafePath)
	}
	if err := atomicRename(temporaryName, entry.full, true); err != nil {
		return fmt.Errorf("write %q: commit temporary file: %w", requested, err)
	}
	committed = true
	if err := syncDirectory(parent.full); err != nil {
		return fmt.Errorf("write %q: sync parent directory: %w", requested, err)
	}
	return nil
}

// WriteStream atomically installs the contents of reader at requested. It is
// the streaming sibling of WriteFile for artifact-sized payloads that must not
// be buffered in memory, and it applies the same size limit, temporary-file
// commit, and revalidation of the parent and destination before the rename.
//
// The reader is consumed at most MaxWriteBytes+1 bytes so an over-long or
// unbounded body is rejected rather than filling the disk. It returns the
// number of bytes written.
func (s *ServerFS) WriteStream(requested string, reader io.Reader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, true)
	if err != nil {
		return 0, fmt.Errorf("write %q: %w", requested, err)
	}
	if entry.clean == "" {
		return 0, fmt.Errorf("write %q: %w", requested, ErrRootMutation)
	}
	if entry.exists && !entry.info.Mode().IsRegular() {
		return 0, fmt.Errorf("write %q: %w", requested, ErrNotRegularFile)
	}

	parent, err := s.resolve(portableParent(entry.clean), false)
	if err != nil {
		return 0, fmt.Errorf("write %q: %w", requested, err)
	}
	if !parent.info.IsDir() {
		return 0, fmt.Errorf("write %q: %w", requested, ErrNotDirectory)
	}

	mode := fs.FileMode(0o640)
	if entry.exists {
		mode = entry.info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(parent.full, ".gugu-write-*")
	if err != nil {
		return 0, fmt.Errorf("write %q: create temporary file: %w", requested, err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(mode); err != nil {
		return 0, fmt.Errorf("write %q: set temporary file mode: %w", requested, err)
	}
	written, err := io.Copy(temporary, io.LimitReader(reader, s.limits.MaxWriteBytes+1))
	if err != nil {
		return 0, fmt.Errorf("write %q: write temporary file: %w", requested, err)
	}
	if written > s.limits.MaxWriteBytes {
		return 0, fmt.Errorf("write %q: %w", requested, ErrSizeLimit)
	}
	if err := temporary.Sync(); err != nil {
		return 0, fmt.Errorf("write %q: sync temporary file: %w", requested, err)
	}
	if err := temporary.Close(); err != nil {
		return 0, fmt.Errorf("write %q: close temporary file: %w", requested, err)
	}

	freshParent, err := s.resolve(parent.clean, false)
	if err != nil || !os.SameFile(parent.info, freshParent.info) {
		return 0, fmt.Errorf("write %q: parent changed during write: %w", requested, ErrUnsafePath)
	}
	freshEntry, err := s.resolve(entry.clean, true)
	if err != nil {
		return 0, fmt.Errorf("write %q: %w", requested, err)
	}
	if entry.exists != freshEntry.exists || entry.exists && !os.SameFile(entry.info, freshEntry.info) {
		return 0, fmt.Errorf("write %q: destination changed during write: %w", requested, ErrUnsafePath)
	}
	if err := atomicRename(temporaryName, entry.full, true); err != nil {
		return 0, fmt.Errorf("write %q: commit temporary file: %w", requested, err)
	}
	committed = true
	if err := syncDirectory(parent.full); err != nil {
		return 0, fmt.Errorf("write %q: sync parent directory: %w", requested, err)
	}
	return written, nil
}

// MkdirAll creates requested and any missing parents, validating each level the
// same way Mkdir does. Existing directories are accepted so an install can be
// retried; an existing non-directory at any level is an error.
func (s *ServerFS) MkdirAll(requested string) error {
	clean, err := NormalizeRelative(requested)
	if err != nil {
		return fmt.Errorf("mkdir %q: %w", requested, err)
	}
	if clean == "" {
		return nil
	}
	components := strings.Split(clean, "/")
	for index := range components {
		prefix := strings.Join(components[:index+1], "/")
		if err := s.Mkdir(prefix); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			existing, statErr := s.Stat(prefix)
			if statErr != nil {
				return statErr
			}
			if !existing.Directory {
				return fmt.Errorf("mkdir %q: %w", prefix, ErrNotDirectory)
			}
		}
	}
	return nil
}

func (s *ServerFS) Mkdir(requested string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, true)
	if err != nil {
		return fmt.Errorf("mkdir %q: %w", requested, err)
	}
	if entry.clean == "" {
		return fmt.Errorf("mkdir %q: %w", requested, ErrRootMutation)
	}
	if entry.exists {
		return fmt.Errorf("mkdir %q: %w", requested, fs.ErrExist)
	}
	parent, err := s.resolve(portableParent(entry.clean), false)
	if err != nil {
		return fmt.Errorf("mkdir %q: %w", requested, err)
	}
	if !parent.info.IsDir() {
		return fmt.Errorf("mkdir %q: %w", requested, ErrNotDirectory)
	}
	if err := os.Mkdir(entry.full, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", requested, err)
	}
	created, err := os.Lstat(entry.full)
	if err != nil || !created.IsDir() || created.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(entry.full)
		return fmt.Errorf("mkdir %q: created entry is unsafe: %w", requested, ErrUnsafePath)
	}
	if err := syncDirectory(parent.full); err != nil {
		return fmt.Errorf("mkdir %q: sync parent directory: %w", requested, err)
	}
	return nil
}

func (s *ServerFS) Move(source string, destination string, replace bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sourceEntry, err := s.resolve(source, false)
	if err != nil {
		return fmt.Errorf("move %q: %w", source, err)
	}
	if sourceEntry.clean == "" {
		return fmt.Errorf("move %q: %w", source, ErrRootMutation)
	}
	destinationEntry, err := s.resolve(destination, true)
	if err != nil {
		return fmt.Errorf("move to %q: %w", destination, err)
	}
	if destinationEntry.clean == "" {
		return fmt.Errorf("move to %q: %w", destination, ErrRootMutation)
	}
	if samePath(sourceEntry.full, destinationEntry.full) {
		return fmt.Errorf("move %q to %q: %w", source, destination, ErrInvalidMove)
	}
	if sourceEntry.info.IsDir() && pathContains(sourceEntry.full, destinationEntry.full) {
		return fmt.Errorf("move %q to %q: directory cannot contain destination: %w", source, destination, ErrInvalidMove)
	}
	if destinationEntry.exists {
		if !replace {
			return fmt.Errorf("move to %q: %w", destination, fs.ErrExist)
		}
		if sourceEntry.info.IsDir() || destinationEntry.info.IsDir() {
			return fmt.Errorf("move %q to %q: directory replacement is not supported: %w", source, destination, ErrInvalidMove)
		}
	}

	sourceParent, err := s.resolve(portableParent(sourceEntry.clean), false)
	if err != nil {
		return fmt.Errorf("move %q: %w", source, err)
	}
	destinationParent, err := s.resolve(portableParent(destinationEntry.clean), false)
	if err != nil {
		return fmt.Errorf("move to %q: %w", destination, err)
	}
	if !sourceParent.info.IsDir() || !destinationParent.info.IsDir() {
		return fmt.Errorf("move %q to %q: %w", source, destination, ErrNotDirectory)
	}
	if err := atomicRename(sourceEntry.full, destinationEntry.full, replace); err != nil {
		return fmt.Errorf("move %q to %q: %w", source, destination, err)
	}
	if err := syncDirectory(sourceParent.full); err != nil {
		return fmt.Errorf("move %q: sync source parent: %w", source, err)
	}
	if !samePath(sourceParent.full, destinationParent.full) {
		if err := syncDirectory(destinationParent.full); err != nil {
			return fmt.Errorf("move to %q: sync destination parent: %w", destination, err)
		}
	}
	return nil
}

func (s *ServerFS) Delete(requested string, recursive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.resolve(requested, false)
	if err != nil {
		return fmt.Errorf("delete %q: %w", requested, err)
	}
	if entry.clean == "" {
		return fmt.Errorf("delete %q: %w", requested, ErrRootMutation)
	}
	parent, err := s.resolve(portableParent(entry.clean), false)
	if err != nil {
		return fmt.Errorf("delete %q: %w", requested, err)
	}

	if !entry.info.IsDir() || !recursive {
		if err := os.Remove(entry.full); err != nil {
			return fmt.Errorf("delete %q: %w", requested, err)
		}
	} else {
		entries, err := s.snapshotTree(entry.full, entry.clean)
		if err != nil {
			return fmt.Errorf("delete %q: %w", requested, err)
		}
		for _, current := range entries {
			info, err := os.Lstat(current.full)
			if err != nil || !os.SameFile(current.info, info) {
				return fmt.Errorf("delete %q: tree changed during preflight: %w", requested, ErrUnsafePath)
			}
		}
		for _, current := range entries {
			if err := os.Remove(current.full); err != nil {
				return fmt.Errorf("delete %q: %w", requested, err)
			}
		}
	}
	if err := syncDirectory(parent.full); err != nil {
		return fmt.Errorf("delete %q: sync parent directory: %w", requested, err)
	}
	return nil
}

func (s *ServerFS) resolve(requested string, allowMissingFinal bool) (resolvedEntry, error) {
	clean, err := NormalizeRelative(requested)
	if err != nil {
		return resolvedEntry{}, err
	}
	if err := validatePortablePath(clean); err != nil {
		return resolvedEntry{}, err
	}
	if err := s.verifyRoot(); err != nil {
		return resolvedEntry{}, err
	}
	if clean == "" {
		return resolvedEntry{clean: "", full: s.root, info: s.rootInfo, exists: true}, nil
	}

	components := strings.Split(clean, "/")
	current := s.root
	for index, component := range components {
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && allowMissingFinal && index == len(components)-1 {
				return resolvedEntry{clean: clean, full: current}, nil
			}
			return resolvedEntry{}, err
		}
		if err := validateEntryMode(info); err != nil {
			return resolvedEntry{}, err
		}
		if index < len(components)-1 && !info.IsDir() {
			return resolvedEntry{}, ErrNotDirectory
		}
		expected := filepath.Join(s.rootReal, filepath.Join(components[:index+1]...))
		realPath, err := filepath.EvalSymlinks(current)
		if err != nil {
			return resolvedEntry{}, err
		}
		if !samePath(realPath, expected) || !pathContainsOrEqual(s.rootReal, realPath) {
			return resolvedEntry{}, ErrUnsafePath
		}
		if index == len(components)-1 {
			return resolvedEntry{clean: clean, full: current, info: info, exists: true}, nil
		}
	}
	return resolvedEntry{}, fs.ErrNotExist
}

func (s *ServerFS) verifyRoot() error {
	info, err := os.Lstat(s.root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(s.rootInfo, info) {
		return ErrUnsafePath
	}
	realRoot, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return err
	}
	if !samePath(realRoot, s.rootReal) {
		return ErrUnsafePath
	}
	return nil
}

func (s *ServerFS) snapshotTree(full string, clean string) ([]treeEntry, error) {
	entry, err := s.resolve(clean, false)
	if err != nil {
		return nil, err
	}
	if !samePath(full, entry.full) {
		return nil, ErrUnsafePath
	}
	if !entry.info.IsDir() {
		return []treeEntry{{full: entry.full, info: entry.info}}, nil
	}

	directory, err := os.Open(entry.full)
	if err != nil {
		return nil, err
	}
	openedInfo, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if !openedInfo.IsDir() || !os.SameFile(entry.info, openedInfo) {
		_ = directory.Close()
		return nil, ErrUnsafePath
	}
	children, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}

	var result []treeEntry
	for _, child := range children {
		childClean := path.Join(clean, child.Name())
		childEntry, err := s.resolve(childClean, false)
		if err != nil {
			return nil, err
		}
		if childEntry.info.IsDir() {
			nested, err := s.snapshotTree(childEntry.full, childEntry.clean)
			if err != nil {
				return nil, err
			}
			result = append(result, nested...)
		} else {
			result = append(result, treeEntry{full: childEntry.full, info: childEntry.info})
		}
	}
	return append(result, treeEntry{full: entry.full, info: entry.info}), nil
}

func validateEntryMode(info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	if info.IsDir() || info.Mode().IsRegular() {
		return nil
	}
	return ErrUnsupportedFileType
}

func validatePortablePath(clean string) error {
	if clean == "" {
		return nil
	}
	for _, component := range strings.Split(clean, "/") {
		if component == "" || strings.Contains(component, ":") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return ErrUnsafePath
		}
		base := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if isWindowsDeviceName(base) {
			return ErrUnsafePath
		}
	}
	return nil
}

func isWindowsDeviceName(base string) bool {
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func portableParent(clean string) string {
	parent := path.Dir(clean)
	if parent == "." {
		return ""
	}
	return parent
}

func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathContains(parent string, child string) bool {
	if samePath(parent, child) {
		return false
	}
	return pathContainsOrEqual(parent, child)
}

func pathContainsOrEqual(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
