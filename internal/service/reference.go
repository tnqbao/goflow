package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"

	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const maxReferenceImageBytes int64 = 10 << 20 // 10 MiB

var referenceFilePattern = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|webp)$`)

// ReferenceImage describes an uploaded raster reference image stored in artifact storage.
type ReferenceImage struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	URL         string `json:"url"`
}

// ReferenceFile is an opened reference image ready to stream.
type ReferenceFile struct {
	Name        string
	ContentType string
	Object      storage.Object
}

// UploadReference validates a PNG/JPEG/WebP image up to 10 MiB and stores it under references/.
func (s *Artifacts) UploadReference(ctx context.Context, source io.Reader) (ReferenceImage, error) {
	if s == nil || s.store == nil {
		return ReferenceImage{}, ErrStorageUnavailable
	}
	if source == nil {
		return ReferenceImage{}, fmt.Errorf("%w: reference image file is required", workflow.ErrInvalid)
	}

	raw, err := io.ReadAll(io.LimitReader(source, maxReferenceImageBytes+1))
	if err != nil {
		return ReferenceImage{}, fmt.Errorf("read reference image: %w", err)
	}
	if len(raw) == 0 || int64(len(raw)) > maxReferenceImageBytes {
		return ReferenceImage{}, fmt.Errorf("%w: reference image must be between 1 byte and 10 MiB", workflow.ErrInvalid)
	}

	ext, contentType, ok := detectReferenceImageType(raw)
	if !ok {
		return ReferenceImage{}, fmt.Errorf("%w: reference image must be PNG, JPEG, or WebP", workflow.ErrInvalid)
	}

	name, err := generateReferenceFileName(ext)
	if err != nil {
		return ReferenceImage{}, err
	}

	key := path.Join("references", name)
	if err := s.store.Put(ctx, key, bytes.NewReader(raw)); err != nil {
		return ReferenceImage{}, fmt.Errorf("store reference image: %w", err)
	}

	return ReferenceImage{
		Key:         key,
		Name:        name,
		ContentType: contentType,
		SizeBytes:   int64(len(raw)),
		URL:         "/api/v1/artifacts/references/" + name,
	}, nil
}

// OpenReference opens a previously uploaded reference image by validated filename.
func (s *Artifacts) OpenReference(ctx context.Context, name string) (ReferenceFile, error) {
	if s == nil || s.store == nil {
		return ReferenceFile{}, ErrStorageUnavailable
	}
	if !referenceFilePattern.MatchString(name) {
		return ReferenceFile{}, ErrArtifactNotFound
	}

	key := path.Join("references", name)
	object, err := s.store.Open(ctx, key)
	if err != nil {
		return ReferenceFile{}, artifactStorageError(err)
	}

	return ReferenceFile{
		Name:        name,
		ContentType: referenceContentType(name),
		Object:      object,
	}, nil
}

func detectReferenceImageType(raw []byte) (string, string, bool) {
	detected := http.DetectContentType(raw)
	switch detected {
	case "image/png":
		return ".png", "image/png", true
	case "image/jpeg":
		return ".jpg", "image/jpeg", true
	case "image/webp":
		return ".webp", "image/webp", true
	default:
		return "", "", false
	}
}

func referenceContentType(name string) string {
	switch path.Ext(name) {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func generateReferenceFileName(ext string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate reference image id: %w", err)
	}
	return hex.EncodeToString(raw[:]) + ext, nil
}

// ReferenceKeyValid reports whether a VideoRun input key refers to a stored reference image.
func ReferenceKeyValid(key string) bool {
	if len(key) <= len("references/") {
		return false
	}
	if key[:len("references/")] != "references/" {
		return false
	}
	return referenceFilePattern.MatchString(key[len("references/"):])
}

// Ensure errors import stays used cleanly if expanded.
var _ = errors.Is
