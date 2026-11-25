package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newTestStore(t *testing.T, max int64, retention time.Duration) (*Filesystem, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewFilesystem(root, max, retention)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, root
}

func readArtifact(t *testing.T, store Store, key string) string {
	t.Helper()
	object, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(object)
	if err := errors.Join(readErr, object.Close()); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFilesystemPutOpenDelete(t *testing.T) {
	store, root := newTestStore(t, 32, 0)
	ctx := context.Background()
	key := "workflows/123/scene.svg"
	if got, err := store.Key(filepath.Join(root, key)); err != nil || got != key {
		t.Fatalf("key = %q, %v", got, err)
	}
	if err := store.Put(ctx, key, strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := object.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(object)
	if err != nil || string(data) != "rst" {
		t.Fatalf("seek result = %q, %v", data, err)
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, key, strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	if got := readArtifact(t, store, key); got != "second" {
		t.Fatalf("artifact = %q", got)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted artifact: %v", err)
	}
}

func TestFilesystemRejectsEscapesAndSymlinks(t *testing.T) {
	store, root := newTestStore(t, 64, 0)
	ctx := context.Background()
	for _, key := range []string{"", ".", "../escape", "a/../b", "/absolute", "a//b", `a\b`, "a\x00b", strings.Repeat("x", 254)} {
		if err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v", key, err)
		}
	}
	if _, err := store.Key(filepath.Join(root, "..", "outside")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("outside key: %v", err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "link/secret", strings.NewReader("changed")); err == nil {
		t.Fatal("write through symlink succeeded")
	}
	if _, err := store.Open(ctx, "link/secret"); err == nil {
		t.Fatal("read through symlink succeeded")
	}
	if err := os.Symlink(secret, filepath.Join(root, "final")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "final"); err == nil {
		t.Fatal("read final symlink succeeded")
	}
	if err := store.Put(ctx, "final", strings.NewReader("inside")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside file = %q, %v", data, err)
	}
}

func TestFilesystemBoundsAndCancellation(t *testing.T) {
	store, root := newTestStore(t, 4, 0)
	ctx := context.Background()
	if err := store.Put(ctx, "item", strings.NewReader("good")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "item", strings.NewReader("large")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}
	if got := readArtifact(t, store, "item"); got != "good" {
		t.Fatalf("failed write replaced artifact: %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "external"), []byte("large"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "external"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized read: %v", err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, "fifo"); err == nil {
		t.Fatal("accepted FIFO")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(cancelled, "item", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if _, err := store.Open(cancelled, "item"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	object, err := store.Open(ctx, "item")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "item"), []byte("extended"), 0600); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(object)
	if err := errors.Join(readErr, object.Close()); err != nil || len(data) > 4 {
		t.Fatalf("grown artifact read = %q, %v", data, err)
	}
}

func TestFilesystemKeepsOpenedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	store, err := NewFilesystem(root, 32, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	oldRoot := filepath.Join(parent, "old-root")
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "item", strings.NewReader("inside")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(oldRoot, "item"))
	if err != nil || string(data) != "inside" {
		t.Fatalf("original root artifact = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "item")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("artifact escaped root: %v", err)
	}
}

func TestFilesystemRetention(t *testing.T) {
	store, root := newTestStore(t, 32, time.Hour)
	ctx := context.Background()
	if err := store.Put(ctx, "item", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	completed := now.Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "item"), completed.Add(-time.Minute), completed.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteExpired(ctx, "item", completed, completed.Add(30*time.Minute)); err != nil || deleted {
		t.Fatalf("unexpired = %v, %v", deleted, err)
	}
	if deleted, err := store.DeleteExpired(ctx, "item", completed, now); err != nil || !deleted {
		t.Fatalf("expired = %v, %v", deleted, err)
	}
	if err := store.Put(ctx, "newer", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteExpired(ctx, "newer", completed, now); err == nil || deleted {
		t.Fatalf("changed artifact = %v, %v", deleted, err)
	}
}

func TestFilesystemAtomicConcurrentPublication(t *testing.T) {
	store, _ := newTestStore(t, 32768, 0)
	ctx := context.Background()
	first := bytes.Repeat([]byte("a"), 16384)
	second := bytes.Repeat([]byte("b"), 16384)
	if err := store.Put(ctx, "item", bytes.NewReader(first)); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, data := range [][]byte{first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 20; i++ {
				if err := store.Put(ctx, "item", bytes.NewReader(data)); err != nil {
					errorsCh <- err
					return
				}
			}
		}()
	}
	defer group.Wait()
	for i := 0; i < 40; i++ {
		got := []byte(readArtifact(t, store, "item"))
		if !bytes.Equal(got, first) && !bytes.Equal(got, second) {
			t.Fatal("observed a partial artifact")
		}
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
}
