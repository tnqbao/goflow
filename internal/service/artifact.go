package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var ErrArtifactNotFound = errors.New("artifact not found")
var ErrArtifactRunActive = errors.New("artifact belongs to a run that still needs its outputs")

type Artifacts struct {
	runs  *repository.ExecutionRepository
	store storage.Store
}

func NewArtifacts(runs *repository.ExecutionRepository, store storage.Store) *Artifacts {
	return &Artifacts{runs: runs, store: store}
}

type Artifact struct {
	ID          string    `json:"id"`
	RunID       string    `json:"runId"`
	Name        string    `json:"name"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	ModifiedAt  time.Time `json:"modifiedAt"`
}

type ArtifactFile struct {
	Artifact
	Object storage.Object
}

type artifactReference struct {
	id, key string
}

func (s *Artifacts) Open(ctx context.Context, id string) (ArtifactFile, error) {
	run, ref, err := s.lookup(ctx, id)
	if err != nil {
		return ArtifactFile{}, err
	}
	return s.open(ctx, run.GetName(), ref)
}

func (s *Artifacts) Delete(ctx context.Context, id string) error {
	run, ref, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	if !canDeleteRunArtifact(executionView(run)) {
		return ErrArtifactRunActive
	}
	active, err := s.runs.HasActiveJobs(ctx, run)
	if err != nil {
		return err
	}
	if active {
		return ErrArtifactRunActive
	}
	return artifactStorageError(s.store.Delete(ctx, ref.key))
}

func canDeleteRunArtifact(run Execution) bool {
	switch run.Phase {
	case "Succeeded", "Failed", "Cancelled":
	default:
		return false
	}
	for _, step := range run.Steps {
		switch step.Phase {
		case "Running", "Cancelling", "RequiresReview":
			return false
		}
	}
	return true
}

func (s *Artifacts) lookup(ctx context.Context, id string) (*unstructured.Unstructured, artifactReference, error) {
	runID, err := artifactRunID(id)
	if err != nil {
		return nil, artifactReference{}, err
	}
	run, err := s.getRun(ctx, runID)
	if err != nil {
		return nil, artifactReference{}, err
	}
	for _, ref := range runArtifactReferences(run) {
		if ref.id == id {
			return run, ref, nil
		}
	}
	return nil, artifactReference{}, ErrArtifactNotFound
}

func (s *Artifacts) getRun(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	if s == nil || s.runs == nil || s.store == nil {
		return nil, ErrStorageUnavailable
	}
	run, err := s.runs.Get(ctx, id)
	if apierrors.IsNotFound(err) {
		return nil, ErrArtifactNotFound
	}
	return run, err
}

func (s *Artifacts) open(ctx context.Context, runID string, ref artifactReference) (ArtifactFile, error) {
	object, err := s.store.Open(ctx, ref.key)
	if err != nil {
		return ArtifactFile{}, artifactStorageError(err)
	}
	info, err := object.Stat()
	if err != nil {
		return ArtifactFile{}, errors.Join(artifactStorageError(err), object.Close())
	}
	name, contentType, err := artifactMedia(ref.key, object)
	if err != nil {
		return ArtifactFile{}, errors.Join(err, object.Close())
	}
	return ArtifactFile{Artifact: Artifact{
		ID: ref.id, RunID: runID, Name: name, ContentType: contentType, SizeBytes: info.Size(), ModifiedAt: info.ModTime(),
	}, Object: object}, nil
}

func artifactStorageError(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, storage.ErrInvalidKey) {
		return ErrArtifactNotFound
	}
	return err
}

func artifactRunID(id string) (string, error) {
	name, hash, ok := strings.Cut(id, ".")
	if !ok || len(name) > 338 || len(hash) != 64 {
		return "", ErrArtifactNotFound
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", ErrArtifactNotFound
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(name)
	if err != nil || validateWorkflowID(string(decoded)) != nil {
		return "", ErrArtifactNotFound
	}
	return string(decoded), nil
}

func artifactID(run *unstructured.Unstructured, key string) string {
	digest := sha256.Sum256([]byte(string(run.GetUID()) + "\x00" + key))
	return base64.RawURLEncoding.EncodeToString([]byte(run.GetName())) + "." + fmt.Sprintf("%x", digest)
}

func runArtifactReferences(run *unstructured.Unstructured) []artifactReference {
	if run.GetUID() == "" {
		return nil
	}
	refs, _, _ := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	steps, _, _ := unstructured.NestedSlice(run.Object, "spec", "workflowSnapshot", "steps")
	types := make(map[string]string, len(steps))
	for _, entry := range steps {
		step, ok := entry.(map[string]any)
		if ok {
			types[statusString(step, "name")] = statusString(step, "type")
		}
	}
	allowed := make(map[string]bool, len(steps))
	for _, step := range executionSteps(run) {
		if statusString(step, "phase") != "Succeeded" {
			continue
		}
		name := statusString(step, "name")
		kind := types[name]
		switch kind {
		case "image", "llm", "video", "ffmpeg":
		default:
			continue
		}
		attempt, _, _ := unstructured.NestedInt64(step, "attempts")
		if attempt < 1 || attempt > 100 {
			continue
		}
		provider, _, _ := unstructured.NestedString(step, "jobConfig", "providerName")
		id := workflow.StepWorkID(string(run.GetUID()), name, attempt)
		key := path.Join("workflows", id, workflow.StepOutputFile(kind, provider != ""))
		allowed[key] = true
	}
	items := make([]artifactReference, 0, len(refs))
	for _, key := range refs {
		if !allowed[key] {
			continue
		}
		delete(allowed, key)
		items = append(items, artifactReference{id: artifactID(run, key), key: key})
	}
	return items
}

func artifactContentType(key string) string {
	switch path.Ext(key) {
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// Raster artifacts share a storage filename even when their encoding differs.
func artifactMedia(key string, object io.ReadSeeker) (string, string, error) {
	name := path.Base(key)
	if path.Ext(name) != ".png" {
		return name, artifactContentType(key), nil
	}
	var header [512]byte
	n, err := io.ReadFull(object, header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", "", fmt.Errorf("read artifact media header: %w", err)
	}
	if _, err := object.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("rewind artifact: %w", err)
	}
	switch http.DetectContentType(header[:n]) {
	case "image/png":
		return name, "image/png", nil
	case "image/jpeg":
		return strings.TrimSuffix(name, ".png") + ".jpg", "image/jpeg", nil
	case "image/webp":
		return strings.TrimSuffix(name, ".png") + ".webp", "image/webp", nil
	default:
		return name, "application/octet-stream", nil
	}
}
