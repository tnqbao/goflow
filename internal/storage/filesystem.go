package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Filesystem stores artifacts below an open, trusted directory on a PVC.
type Filesystem struct {
	mu        sync.RWMutex
	root      string
	rootFD    int
	maxBytes  int64
	retention time.Duration
}

// NewFilesystem opens a storage root; a zero retention disables expiry.
func NewFilesystem(root string, maxBytes int64, retention time.Duration) (*Filesystem, error) {
	if root == "" || maxBytes < 1 || maxBytes == math.MaxInt64 || retention < 0 {
		return nil, fmt.Errorf("invalid artifact storage configuration")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	fd, err := unix.Open(absolute, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	return &Filesystem{root: absolute, rootFD: fd, maxBytes: maxBytes, retention: retention}, nil
}

// Key converts an absolute path under the root or a relative key into a safe key.
func (s *Filesystem) Key(name string) (string, error) {
	if filepath.IsAbs(name) {
		relative, err := filepath.Rel(s.root, name)
		if err != nil {
			return "", fmt.Errorf("resolve artifact key: %w", err)
		}
		name = filepath.ToSlash(relative)
	}
	if err := validKey(name); err != nil {
		return "", err
	}
	return name, nil
}

func validKey(key string) error {
	if key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, "\\\x00") || len(key) > 253 {
		return ErrInvalidKey
	}
	return nil
}

func (s *Filesystem) directory(key string, create bool) (int, string, error) {
	if err := validKey(key); err != nil {
		return -1, "", err
	}
	s.mu.RLock()
	if s.rootFD < 0 {
		s.mu.RUnlock()
		return -1, "", ErrClosed
	}
	fd, err := unix.Openat(s.rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	s.mu.RUnlock()
	if err != nil {
		return -1, "", fmt.Errorf("open artifact root: %w", err)
	}
	parts := strings.Split(key, "/")
	for _, part := range parts[:len(parts)-1] {
		next, openErr := openDirectory(fd, part, create)
		closeErr := unix.Close(fd)
		if openErr != nil {
			return -1, "", errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return -1, "", errors.Join(closeErr, unix.Close(next))
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func openDirectory(parent int, name string, create bool) (int, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if !create || !errors.Is(err, unix.ENOENT) {
		return fd, err
	}
	if err := unix.Mkdirat(parent, name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	return unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// Put publishes an artifact atomically after enforcing the configured limit.
func (s *Filesystem) Put(ctx context.Context, key string, source io.Reader) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("artifact source is required")
	}
	dir, name, err := s.directory(key, true)
	if err != nil {
		return fmt.Errorf("open artifact directory: %w", err)
	}
	defer func() { result = errors.Join(result, unix.Close(dir)) }()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("generate temporary artifact name: %w", err)
	}
	temporary := ".kubereel-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dir, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create temporary artifact: %w", err)
	}
	defer func() {
		if err := unix.Unlinkat(dir, temporary, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			result = errors.Join(result, fmt.Errorf("remove temporary artifact: %w", err))
		}
	}()
	file := os.NewFile(uintptr(fd), temporary)
	written, writeErr := io.Copy(file, io.LimitReader(contextReader{ctx: ctx, source: source}, s.maxBytes+1))
	if writeErr != nil {
		return errors.Join(fmt.Errorf("write artifact: %w", writeErr), file.Close())
	}
	if written > s.maxBytes {
		return errors.Join(ErrTooLarge, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync artifact: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat(dir, temporary, dir, name); err != nil {
		return fmt.Errorf("publish artifact: %w", err)
	}
	if err := unix.Fsync(dir); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}

// Open rejects symlinks, non-regular files, and oversized artifacts.
func (s *Filesystem) Open(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, name, err := s.directory(key, false)
	if err != nil {
		return nil, fmt.Errorf("open artifact directory: %w", err)
	}
	fd, openErr := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	closeErr := unix.Close(dir)
	if openErr != nil {
		return nil, errors.Join(fmt.Errorf("open artifact: %w", openErr), closeErr)
	}
	file := os.NewFile(uintptr(fd), path.Base(key))
	if closeErr != nil {
		return nil, errors.Join(closeErr, file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat artifact: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("artifact is not a regular file"), file.Close())
	}
	if info.Size() > s.maxBytes {
		return nil, errors.Join(ErrTooLarge, file.Close())
	}
	return &boundedObject{file: file, ctx: ctx, maxBytes: s.maxBytes}, nil
}

type boundedObject struct {
	mu       sync.Mutex
	file     *os.File
	ctx      context.Context
	maxBytes int64
}

func (o *boundedObject) Read(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	offset, err := o.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if offset >= o.maxBytes {
		return 0, io.EOF
	}
	if int64(len(data)) > o.maxBytes-offset {
		data = data[:o.maxBytes-offset]
	}
	return o.file.Read(data)
}

func (o *boundedObject) Seek(offset int64, whence int) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	return o.file.Seek(offset, whence)
}

func (o *boundedObject) Stat() (os.FileInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.ctx.Err(); err != nil {
		return nil, err
	}
	info, err := o.file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > o.maxBytes {
		return nil, ErrTooLarge
	}
	return info, nil
}

func (o *boundedObject) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.file.Close()
}

// Delete removes one artifact without following symlinks.
func (s *Filesystem) Delete(ctx context.Context, key string) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, name, err := s.directory(key, false)
	if err != nil {
		return fmt.Errorf("open artifact directory: %w", err)
	}
	defer func() { result = errors.Join(result, unix.Close(dir)) }()
	if err := unix.Unlinkat(dir, name, 0); err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	if err := unix.Fsync(dir); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

// DeleteExpired deletes an artifact only after its owning run has completed and
// the configured retention has elapsed. The caller must ensure the run is terminal
// and no further writes to this key are possible.
func (s *Filesystem) DeleteExpired(ctx context.Context, key string, completedAt, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if completedAt.IsZero() || now.Before(completedAt) {
		return false, fmt.Errorf("invalid completion time")
	}
	if err := validKey(key); err != nil {
		return false, err
	}
	if s.retention == 0 || now.Sub(completedAt) < s.retention {
		return false, nil
	}
	object, err := s.Open(ctx, key)
	if err != nil {
		return false, err
	}
	info, statErr := object.Stat()
	closeErr := object.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return false, fmt.Errorf("inspect artifact for expiry: %w", err)
	}
	if info.ModTime().After(completedAt) {
		return false, fmt.Errorf("artifact changed after run completion")
	}
	if err := s.Delete(ctx, key); err != nil {
		return false, err
	}
	return true, nil
}

// Close releases the root directory descriptor.
func (s *Filesystem) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rootFD < 0 {
		return nil
	}
	err := unix.Close(s.rootFD)
	s.rootFD = -1
	return err
}
