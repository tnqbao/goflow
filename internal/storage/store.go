// Package storage provides bounded artifact storage.
package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

var (
	ErrInvalidKey = errors.New("invalid artifact key")
	ErrTooLarge   = errors.New("artifact exceeds size limit")
	ErrClosed     = errors.New("artifact storage is closed")
)

// Object is an open artifact with seek support for HTTP range requests.
type Object interface {
	io.ReadSeeker
	io.Closer
	Stat() (os.FileInfo, error)
}

// Store manages artifacts addressed by relative keys.
type Store interface {
	Put(context.Context, string, io.Reader) error
	Open(context.Context, string) (Object, error)
	Delete(context.Context, string) error
	DeleteExpired(context.Context, string, time.Time, time.Time) (bool, error)
}

// ManagedStore extends Store with CLI path-to-key resolution and lifecycle cleanup.
type ManagedStore interface {
	Store
	io.Closer
	Key(targetPath string) (string, error)
}
