package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || root == "" {
		return nil, errors.New("local object store root is required")
	}
	if err := os.MkdirAll(filepath.Join(absolute, "objects"), 0o750); err != nil {
		return nil, fmt.Errorf("create local object store: %w", err)
	}
	return &Local{root: absolute}, nil
}

func (s *Local) objectPath(key string) (string, error) {
	if key == "" || strings.Contains(key, "\\") || strings.HasPrefix(key, "/") {
		return "", errors.New("invalid object key")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(key)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != key {
		return "", errors.New("invalid object key")
	}
	path := filepath.Join(s.root, "objects", filepath.FromSlash(clean))
	root := filepath.Join(s.root, "objects") + string(os.PathSeparator)
	if !strings.HasPrefix(path+string(os.PathSeparator), root) {
		return "", errors.New("object key escapes root")
	}
	return path, nil
}

func (s *Local) Put(ctx context.Context, key string, source io.Reader, size int64, _ PutOptions) (ObjectInfo, error) {
	destination, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return ObjectInfo{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".object-*.partial")
	if err != nil {
		return ObjectInfo{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), &contextReader{ctx: ctx, reader: source})
	if closeErr := temporary.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return ObjectInfo{}, copyErr
	}
	if size >= 0 && written != size {
		return ObjectInfo{}, fmt.Errorf("object size mismatch: wrote %d, expected %d", written, size)
	}
	if _, err := os.Stat(destination); err == nil {
		existing, statErr := s.Stat(ctx, key)
		if statErr != nil {
			return ObjectInfo{}, statErr
		}
		if existing.Size != written {
			return ObjectInfo{}, errors.New("immutable object key already contains different content")
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return ObjectInfo{}, err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: written, ETag: hex.EncodeToString(hash.Sum(nil)), LastModified: info.ModTime().UTC()}, nil
}

func (s *Local) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, ObjectInfo{}, err
	}
	return &contextReadCloser{ctx: ctx, ReadCloser: file}, ObjectInfo{Key: key, Size: info.Size(), LastModified: info.ModTime().UTC()}, nil
}

func (s *Local) Stat(_ context.Context, key string) (ObjectInfo, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: info.Size(), LastModified: info.ModTime().UTC()}, nil
}

func (s *Local) Delete(_ context.Context, key string) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Local) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if prefix != "" {
		if _, err := s.objectPath(prefix); err != nil {
			return nil, err
		}
	}
	base := filepath.Join(s.root, "objects")
	result := []ObjectInfo{}
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result = append(result, ObjectInfo{Key: key, Size: info.Size(), LastModified: info.ModTime().UTC()})
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type contextReadCloser struct {
	ctx context.Context
	io.ReadCloser
}

func (r *contextReadCloser) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(buffer)
}
