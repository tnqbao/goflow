package provider

import (
	"context"
	"errors"
	"io"
)

// ErrSubmissionUncertain means a provider may have accepted a request even
// though the caller did not receive its identifier. Callers must not retry the
// submission automatically. This includes cancellation or timeout after a
// request may have reached the upstream service.
var ErrSubmissionUncertain = errors.New("provider submission outcome is uncertain")

// GenerationHandle identifies a provider request. It must be serializable and
// must not contain credentials or other secrets.
type GenerationHandle struct {
	ID string `json:"id"`
}

// GenerationState is the observed state of an asynchronous request.
type GenerationState string

const (
	GenerationPending   GenerationState = "pending"
	GenerationRunning   GenerationState = "running"
	GenerationSucceeded GenerationState = "succeeded"
	GenerationFailed    GenerationState = "failed"
	GenerationCancelled GenerationState = "cancelled"
)

// GenerationStatus describes an asynchronous request without provider-specific data.
type GenerationStatus struct {
	State GenerationState
	// Failure is a sanitized description; it must not contain credentials.
	Failure string
}

// TextRequest describes a text generation request.
type TextRequest struct {
	Model  string
	Prompt string
}

// TextResult contains generated text.
type TextResult struct {
	Text string
}

// TextProvider generates text. Implementations must respect context cancellation.
type TextProvider interface {
	Generate(ctx context.Context, request TextRequest) (TextResult, error)
}

// ImageRequest describes an image generation request.
type ImageRequest struct {
	Model  string
	Prompt string
}

// VideoRequest describes a video generation request.
type VideoRequest struct {
	Model  string
	Prompt string
	// Image is optional input for image-to-video generation.
	Image io.Reader
}

// AudioRequest describes an audio generation request.
type AudioRequest struct {
	Model  string
	Prompt string
}

// MediaResult streams generated media. The caller must close Body and enforce
// the destination's size limit when consuming it.
type MediaResult struct {
	Body        io.ReadCloser
	ContentType string
}

// ImageProvider submits and observes asynchronous image generation requests.
type ImageProvider interface {
	Submit(ctx context.Context, request ImageRequest) (GenerationHandle, error)
	GetStatus(ctx context.Context, handle GenerationHandle) (GenerationStatus, error)
	GetResult(ctx context.Context, handle GenerationHandle) (MediaResult, error)
}

// VideoProvider submits and observes asynchronous video generation requests.
type VideoProvider interface {
	Submit(ctx context.Context, request VideoRequest) (GenerationHandle, error)
	GetStatus(ctx context.Context, handle GenerationHandle) (GenerationStatus, error)
	GetResult(ctx context.Context, handle GenerationHandle) (MediaResult, error)
}

// AudioProvider submits and observes asynchronous audio generation requests.
type AudioProvider interface {
	Submit(ctx context.Context, request AudioRequest) (GenerationHandle, error)
	GetStatus(ctx context.Context, handle GenerationHandle) (GenerationStatus, error)
	GetResult(ctx context.Context, handle GenerationHandle) (MediaResult, error)
}

// Canceller is implemented only by adapters whose documented API supports cancellation.
type Canceller interface {
	Cancel(ctx context.Context, handle GenerationHandle) error
}
