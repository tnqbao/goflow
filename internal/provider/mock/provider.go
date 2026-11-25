// Package mock implements local, deterministic provider behavior for testing.
package mock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
)

var (
	ErrRateLimited = errors.New("mock provider rate limited")
	ErrNotFound    = errors.New("mock generation not found")
	ErrNotReady    = errors.New("mock generation is not ready")
	ErrFailed      = errors.New("mock generation failed")
	ErrCapacity    = errors.New("mock generation capacity reached")
)

// Options controls simulated latency and failure behavior.
type Options struct {
	Latency   time.Duration
	Fail      bool
	RateLimit bool
}

type generation struct {
	kind      string
	prompt    string
	readyAt   time.Time
	cancelled bool
}

// Provider stores a bounded set of simulated generations in memory.
// Handles are valid for the lifetime of this instance.
type Provider struct {
	mu       sync.Mutex
	options  Options
	requests map[string]*generation
}

const maxRequests = 1024

func New(options Options) (*Provider, error) {
	if options.Latency < 0 {
		return nil, fmt.Errorf("mock latency must not be negative")
	}
	return &Provider{options: options, requests: make(map[string]*generation)}, nil
}

// Generate returns a deterministic sample script without contacting a service.
func (p *Provider) Generate(ctx context.Context, req provider.TextRequest) (provider.TextResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.TextResult{}, err
	}
	prompt, err := validPrompt(req.Prompt)
	if err != nil {
		return provider.TextResult{}, err
	}
	if p.options.RateLimit {
		return provider.TextResult{}, ErrRateLimited
	}
	if err := wait(ctx, p.options.Latency); err != nil {
		return provider.TextResult{}, err
	}
	if p.options.Fail {
		return provider.TextResult{}, ErrFailed
	}
	return provider.TextResult{Text: "Sample script: " + prompt}, nil
}

func wait(ctx context.Context, duration time.Duration) error {
	if duration == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Image returns the image generation capability.
func (p *Provider) Image() *Image { return &Image{provider: p} }

// Video returns the video generation capability.
func (p *Provider) Video() *Video { return &Video{provider: p} }

type Image struct{ provider *Provider }
type Video struct{ provider *Provider }

var _ provider.TextProvider = (*Provider)(nil)
var _ provider.ImageProvider = (*Image)(nil)
var _ provider.VideoProvider = (*Video)(nil)
var _ provider.Canceller = (*Image)(nil)
var _ provider.Canceller = (*Video)(nil)

func (p *Provider) submit(ctx context.Context, kind, input string) (provider.GenerationHandle, error) {
	if err := ctx.Err(); err != nil {
		return provider.GenerationHandle{}, err
	}
	prompt, err := validPrompt(input)
	if err != nil {
		return provider.GenerationHandle{}, err
	}
	if p.options.RateLimit {
		return provider.GenerationHandle{}, ErrRateLimited
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.GenerationHandle{}, err
	}
	if len(p.requests) >= maxRequests {
		return provider.GenerationHandle{}, ErrCapacity
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return provider.GenerationHandle{}, fmt.Errorf("generate mock request ID: %w", err)
	}
	id := hex.EncodeToString(random[:])
	p.requests[id] = &generation{kind: kind, prompt: prompt, readyAt: time.Now().Add(p.options.Latency)}
	return provider.GenerationHandle{ID: id}, nil
}

func (p *Provider) status(ctx context.Context, kind string, handle provider.GenerationHandle) (provider.GenerationStatus, string, error) {
	if err := ctx.Err(); err != nil {
		return provider.GenerationStatus{}, "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	request, ok := p.requests[handle.ID]
	if !ok || request.kind != kind {
		return provider.GenerationStatus{}, "", ErrNotFound
	}
	if request.cancelled {
		return provider.GenerationStatus{State: provider.GenerationCancelled}, "", nil
	}
	if time.Now().Before(request.readyAt) {
		return provider.GenerationStatus{State: provider.GenerationRunning}, "", nil
	}
	if p.options.Fail {
		return provider.GenerationStatus{State: provider.GenerationFailed, Failure: ErrFailed.Error()}, "", nil
	}
	return provider.GenerationStatus{State: provider.GenerationSucceeded}, request.prompt, nil
}

func (p *Provider) cancel(ctx context.Context, kind string, handle provider.GenerationHandle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	request, ok := p.requests[handle.ID]
	if !ok || request.kind != kind {
		return ErrNotFound
	}
	if !time.Now().Before(request.readyAt) {
		return fmt.Errorf("mock generation already finished")
	}
	request.cancelled = true
	return nil
}

// Forget releases a terminal generation from the bounded in-memory store.
func (p *Provider) Forget(ctx context.Context, handle provider.GenerationHandle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	request, ok := p.requests[handle.ID]
	if !ok {
		return ErrNotFound
	}
	if !request.cancelled && time.Now().Before(request.readyAt) {
		return ErrNotReady
	}
	delete(p.requests, handle.ID)
	return nil
}

func validPrompt(input string) (string, error) {
	prompt := strings.TrimSpace(input)
	if prompt == "" || len(prompt) > 8192 {
		return "", fmt.Errorf("mock prompt must contain 1 to 8192 bytes")
	}
	return prompt, nil
}

// SVG creates the same sample image for the same prompt.
func SVG(input string) ([]byte, error) {
	prompt, err := validPrompt(input)
	if err != nil {
		return nil, err
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(prompt)); err != nil {
		return nil, fmt.Errorf("escape prompt: %w", err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720"><rect width="1280" height="720" fill="#18202b"/><text x="64" y="90" fill="#fff" font-family="sans-serif" font-size="32">Mock scene</text><text x="64" y="150" fill="#fff" font-family="sans-serif" font-size="24">` + escaped.String() + `</text></svg>`
	return []byte(svg), nil
}

func (i *Image) Submit(ctx context.Context, req provider.ImageRequest) (provider.GenerationHandle, error) {
	return i.provider.submit(ctx, "image", req.Prompt)
}
func (i *Image) GetStatus(ctx context.Context, handle provider.GenerationHandle) (provider.GenerationStatus, error) {
	state, _, err := i.provider.status(ctx, "image", handle)
	return state, err
}
func (i *Image) Cancel(ctx context.Context, handle provider.GenerationHandle) error {
	return i.provider.cancel(ctx, "image", handle)
}
func (i *Image) GetResult(ctx context.Context, handle provider.GenerationHandle) (provider.MediaResult, error) {
	state, prompt, err := i.provider.status(ctx, "image", handle)
	if err != nil {
		return provider.MediaResult{}, err
	}
	if err := resultReady(state); err != nil {
		return provider.MediaResult{}, err
	}
	data, err := SVG(prompt)
	if err != nil {
		return provider.MediaResult{}, err
	}
	return provider.MediaResult{Body: io.NopCloser(bytes.NewReader(data)), ContentType: "image/svg+xml"}, nil
}

func (v *Video) Submit(ctx context.Context, req provider.VideoRequest) (provider.GenerationHandle, error) {
	if req.Image != nil {
		return provider.GenerationHandle{}, fmt.Errorf("mock image-to-video is not supported")
	}
	return v.provider.submit(ctx, "video", req.Prompt)
}
func (v *Video) GetStatus(ctx context.Context, handle provider.GenerationHandle) (provider.GenerationStatus, error) {
	state, _, err := v.provider.status(ctx, "video", handle)
	return state, err
}
func (v *Video) Cancel(ctx context.Context, handle provider.GenerationHandle) error {
	return v.provider.cancel(ctx, "video", handle)
}
func (v *Video) GetResult(ctx context.Context, handle provider.GenerationHandle) (provider.MediaResult, error) {
	state, _, err := v.provider.status(ctx, "video", handle)
	if err != nil {
		return provider.MediaResult{}, err
	}
	if err := resultReady(state); err != nil {
		return provider.MediaResult{}, err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=320x240:r=10:d=1", "-an", "-c:v", "mpeg4", "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	data, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		return provider.MediaResult{}, ctx.Err()
	}
	if err != nil {
		return provider.MediaResult{}, fmt.Errorf("generate mock video: %w", err)
	}
	return provider.MediaResult{Body: io.NopCloser(bytes.NewReader(data)), ContentType: "video/mp4"}, nil
}

func resultReady(status provider.GenerationStatus) error {
	switch status.State {
	case provider.GenerationSucceeded:
		return nil
	case provider.GenerationFailed:
		return ErrFailed
	case provider.GenerationCancelled:
		return context.Canceled
	default:
		return ErrNotReady
	}
}
