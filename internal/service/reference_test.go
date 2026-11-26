package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestArtifactsUploadAndOpenReference(t *testing.T) {
	store, err := storage.NewFilesystem(t.TempDir(), 16<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewArtifacts(nil, store)
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13}

	ref, err := svc.UploadReference(context.Background(), bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("UploadReference(PNG) error = %v", err)
	}
	if !ReferenceKeyValid(ref.Key) {
		t.Fatalf("ReferenceKeyValid(%q) = false", ref.Key)
	}
	if ref.ContentType != "image/png" || ref.SizeBytes != int64(len(pngBytes)) {
		t.Fatalf("unexpected ref metadata = %+v", ref)
	}

	opened, err := svc.OpenReference(context.Background(), ref.Name)
	if err != nil {
		t.Fatalf("OpenReference error = %v", err)
	}
	got, readErr := io.ReadAll(opened.Object)
	if err := errors.Join(readErr, opened.Object.Close()); err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("OpenReference body = %x, %v", got, err)
	}

	if _, err := svc.UploadReference(context.Background(), bytes.NewReader([]byte("not an image"))); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("UploadReference(invalid) = %v, want workflow.ErrInvalid", err)
	}
	if _, err := svc.OpenReference(context.Background(), "../escape.png"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("OpenReference(traversal) = %v, want ErrArtifactNotFound", err)
	}
	if ReferenceKeyValid("references/../bad.png") || ReferenceKeyValid("workflows/123/image.png") {
		t.Fatal("ReferenceKeyValid accepted invalid key")
	}
}
