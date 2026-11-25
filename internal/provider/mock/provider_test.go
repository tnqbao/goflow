package mock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
)

func TestTextAndImageGeneration(t *testing.T) {
	ctx := context.Background()
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	text, err := p.Generate(ctx, provider.TextRequest{Prompt: "hello"})
	if err != nil || text.Text != "Sample script: hello" {
		t.Fatalf("text = %+v, %v", text, err)
	}
	image := p.Image()
	handle, err := image.Submit(ctx, provider.ImageRequest{Prompt: "Cat & <bird>"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := image.GetStatus(ctx, handle)
	if err != nil || state.State != provider.GenerationSucceeded {
		t.Fatalf("status = %+v, %v", state, err)
	}
	result, err := image.GetResult(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(result.Body)
	if err := errors.Join(readErr, result.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/svg+xml" || !strings.Contains(string(data), "Cat &amp; &lt;bird&gt;") {
		t.Fatalf("image = %q, %q", result.ContentType, data)
	}
	other, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Image().Submit(ctx, provider.ImageRequest{Prompt: "Cat & <bird>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Image().GetStatus(ctx, handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("handle from another instance = %v", err)
	}
	if err := p.Forget(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := image.GetStatus(ctx, handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten = %v", err)
	}
}

func TestSimulationAndCancellation(t *testing.T) {
	ctx := context.Background()
	p, err := New(Options{Latency: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	image := p.Image()
	handle, err := image.Submit(ctx, provider.ImageRequest{Prompt: "scene"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := image.GetStatus(ctx, handle)
	if err != nil || state.State != provider.GenerationRunning {
		t.Fatalf("status = %+v, %v", state, err)
	}
	if _, err := image.GetResult(ctx, handle); !errors.Is(err, ErrNotReady) {
		t.Fatalf("early result = %v", err)
	}
	if err := p.Forget(ctx, handle); !errors.Is(err, ErrNotReady) {
		t.Fatalf("forget running = %v", err)
	}
	if err := image.Cancel(ctx, handle); err != nil {
		t.Fatal(err)
	}
	state, err = image.GetStatus(ctx, handle)
	if err != nil || state.State != provider.GenerationCancelled {
		t.Fatalf("cancelled = %+v, %v", state, err)
	}
	if _, err := image.GetResult(ctx, handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result = %v", err)
	}
	if err := p.Forget(ctx, handle); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Generate(cancelled, provider.TextRequest{Prompt: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled text = %v", err)
	}
	if _, err := image.Submit(cancelled, provider.ImageRequest{Prompt: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled submit = %v", err)
	}
	failed, err := New(Options{Fail: true})
	if err != nil {
		t.Fatal(err)
	}
	h, err := failed.Video().Submit(ctx, provider.VideoRequest{Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := failed.Video().GetStatus(ctx, h)
	if err != nil || status.State != provider.GenerationFailed {
		t.Fatalf("failed status = %+v, %v", status, err)
	}
	if _, err := failed.Video().GetResult(ctx, h); !errors.Is(err, ErrFailed) {
		t.Fatalf("failed result = %v", err)
	}
	limited, err := New(Options{RateLimit: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Image().Submit(ctx, provider.ImageRequest{Prompt: "test"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit = %v", err)
	}
	if _, err := limited.Generate(ctx, provider.TextRequest{Prompt: "test"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("text rate limit = %v", err)
	}
}

func TestConcurrentSubmissions(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan provider.GenerationHandle, 32)
	for i := 0; i < cap(results); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			h, err := p.Image().Submit(context.Background(), provider.ImageRequest{Prompt: "same"})
			if err == nil {
				results <- h
			}
		}()
	}
	group.Wait()
	close(results)
	seen := make(map[string]bool)
	for h := range results {
		if seen[h.ID] {
			t.Fatal("duplicate handle")
		}
		seen[h.ID] = true
	}
	if len(seen) != cap(results) {
		t.Fatalf("submissions = %d", len(seen))
	}
}

func TestVideoClip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed on this host")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed on this host")
	}
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.Video().Submit(context.Background(), provider.VideoRequest{Prompt: "scene"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Video().GetResult(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(result.Body)
	if err := errors.Join(readErr, result.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "video/mp4" {
		t.Fatalf("content type = %q", result.ContentType)
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_type", "-of", "default=nw=1:nk=1", "pipe:0")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "video" {
		t.Fatalf("ffprobe = %q, %v", out, err)
	}
}
