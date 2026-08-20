// Package objectstore provides the streaming object boundary used by backups.
// Implementations must treat object keys as opaque relative names and must not
// expose partially written objects after Put returns an error.
package objectstore

import (
	"context"
	"io"
	"time"
)

type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}

type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

type Store interface {
	Put(context.Context, string, io.Reader, int64, PutOptions) (ObjectInfo, error)
	Get(context.Context, string) (io.ReadCloser, ObjectInfo, error)
	Stat(context.Context, string) (ObjectInfo, error)
	Delete(context.Context, string) error
	List(context.Context, string) ([]ObjectInfo, error)
}
