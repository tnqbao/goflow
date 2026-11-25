package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const (
	maxPromptBytes      = 8192
	DefaultProviderID   = "mock"
	DownloadMP4Filename = "mock-video.mp4"
	mp4ContentType      = "video/mp4"
)

var (
	ErrArtifactUnavailable = errors.New("artifact not available")
	ErrStorageUnavailable  = errors.New("artifact storage unavailable")

	workflowIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// ArtifactDownload holds an opened, verified workflow output stream.
type ArtifactDownload struct {
	Object      storage.Object
	Filename    string
	ContentType string
	ModTime     time.Time
}

// WorkflowService implements business rules for creating, querying, and advancing workflows.
type WorkflowService struct {
	repo      *repository.WorkflowRepository
	artifacts storage.Store
	providers *ProviderService
}

// NewWorkflowService constructs a WorkflowService from its repository, storage, and provider dependencies.
func NewWorkflowService(
	repo *repository.WorkflowRepository,
	artifacts storage.Store,
	providers *ProviderService,
) *WorkflowService {
	return &WorkflowService{
		repo:      repo,
		artifacts: artifacts,
		providers: providers,
	}
}

// Create validates the provider and prompt, generates a workflow ID, and queues execution.
func (s *WorkflowService) Create(ctx context.Context, prompt, providerID string) (workflow.Workflow, error) {
	if s == nil || s.repo == nil {
		return workflow.Workflow{}, fmt.Errorf("workflow repository is unavailable")
	}

	selectedProvider := providerID
	if selectedProvider == "" {
		selectedProvider = DefaultProviderID
	}

	if s.providers != nil && !s.providers.IsAvailable(selectedProvider) {
		return workflow.Workflow{}, ErrProviderUnavailable
	}

	trimmedPrompt := strings.TrimSpace(prompt)
	if trimmedPrompt == "" || len(trimmedPrompt) > maxPromptBytes {
		return workflow.Workflow{}, fmt.Errorf("%w: prompt must contain 1 to %d bytes", workflow.ErrInvalid, maxPromptBytes)
	}

	var rawID [16]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return workflow.Workflow{}, fmt.Errorf("generate workflow ID: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Nanosecond)
	created := workflow.Workflow{
		ID:        hex.EncodeToString(rawID[:]),
		Prompt:    trimmedPrompt,
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Insert(ctx, created); err != nil {
		return workflow.Workflow{}, err
	}

	return s.repo.FindByID(ctx, created.ID)
}

// Get validates id format and loads the matching workflow.
func (s *WorkflowService) Get(ctx context.Context, id string) (workflow.Workflow, error) {
	if s == nil || s.repo == nil {
		return workflow.Workflow{}, fmt.Errorf("workflow repository is unavailable")
	}

	if !workflowIDPattern.MatchString(id) {
		return workflow.Workflow{}, workflow.ErrNotFound
	}

	return s.repo.FindByID(ctx, id)
}

// Active lists workflows awaiting or undergoing reconciliation.
func (s *WorkflowService) Active(ctx context.Context) ([]workflow.Workflow, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("workflow repository is unavailable")
	}

	return s.repo.ListActive(ctx)
}

// Transition validates the state transition and updates the workflow status.
func (s *WorkflowService) Transition(ctx context.Context, id, fromStatus, toStatus, result, failure string) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("workflow repository is unavailable")
	}

	if !workflow.IsValidTransition(fromStatus, toStatus, result, failure) {
		return workflow.ErrInvalid
	}

	return s.repo.UpdateStatus(ctx, id, fromStatus, toStatus, result, failure, time.Now().UTC())
}

// OpenArtifact verifies workflow completion and opens its rendered MP4 artifact.
func (s *WorkflowService) OpenArtifact(ctx context.Context, id string) (ArtifactDownload, error) {
	wf, err := s.Get(ctx, id)
	if err != nil {
		return ArtifactDownload{}, err
	}

	expectedPath := filepath.Join("workflows", wf.ID, "final.mp4")
	if wf.Status != "succeeded" || wf.Result != expectedPath {
		return ArtifactDownload{}, ErrArtifactUnavailable
	}

	if s.artifacts == nil {
		return ArtifactDownload{}, ErrStorageUnavailable
	}

	object, err := s.artifacts.Open(ctx, filepath.ToSlash(expectedPath))
	if err != nil {
		return ArtifactDownload{}, ErrArtifactUnavailable
	}

	info, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return ArtifactDownload{}, ErrArtifactUnavailable
	}

	return ArtifactDownload{
		Object:      object,
		Filename:    DownloadMP4Filename,
		ContentType: mp4ContentType,
		ModTime:     info.ModTime(),
	}, nil
}
