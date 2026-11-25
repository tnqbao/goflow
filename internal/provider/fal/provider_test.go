package fal

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
)

func samplePNG() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R'}
}

func TestTextToImageAndQueueLifecycle(t *testing.T) {
	const falKey = "fal_secret_key_abc123"

	var cdnSawAuthHeader atomic.Bool
	cdnServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			cdnSawAuthHeader.Store(true)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(samplePNG())
	}))
	defer cdnServer.Close()

	var statusPollCount atomic.Int32
	var cancelled atomic.Bool

	var queueServer *httptest.Server
	queueServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Key "+falKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"invalid credentials ` + falKey + `"}`))
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/fal-ai/flux/dev":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["prompt"] != "A misty pine forest" || body["image_size"] != "landscape_16_9" {
				t.Errorf("unexpected submit body: %+v", body)
			}
			_, _ = w.Write([]byte(`{
				"request_id": "req-img-001",
				"status": "IN_QUEUE",
				"response_url": "` + queueServer.URL + `/fal-ai/flux/requests/req-img-001",
				"status_url": "` + queueServer.URL + `/fal-ai/flux/requests/req-img-001/status",
				"cancel_url": "` + queueServer.URL + `/fal-ai/flux/requests/req-img-001/cancel"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/fal-ai/flux/requests/req-img-001/status":
			if cancelled.Load() {
				_, _ = w.Write([]byte(`{"status":"CANCELLED"}`))
				return
			}
			call := statusPollCount.Add(1)
			if call == 1 {
				_, _ = w.Write([]byte(`{"status":"IN_QUEUE"}`))
				return
			}
			if call == 2 {
				_, _ = w.Write([]byte(`{"status":"IN_PROGRESS"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/fal-ai/flux/requests/req-img-001":
			_, _ = w.Write([]byte(`{"images":[{"url":"` + cdnServer.URL + `/out.png","content_type":"image/png"}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/fal-ai/flux/requests/req-img-001/cancel":
			cancelled.Store(true)
			_, _ = w.Write([]byte(`{"status":"OK"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer queueServer.Close()

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	adapter, err := newWithHTTPClient(Config{
		APIKey:         falKey,
		BaseURL:        queueServer.URL,
		DefaultModel:   "fal-ai/flux/dev",
		BaseRetryDelay: 5 * time.Millisecond,
		Parameters:     map[string]any{"image_size": "landscape_16_9"},
	}, httpClient)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	img := adapter.Image()

	handle, err := img.Submit(ctx, provider.ImageRequest{Prompt: "  A misty pine forest  "})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if handle.ID != "fal-ai/flux:req-img-001" {
		t.Fatalf("handle = %q", handle.ID)
	}

	st1, err := img.GetStatus(ctx, handle)
	if err != nil || st1.State != provider.GenerationPending {
		t.Fatalf("status 1 = %+v, %v", st1, err)
	}

	if _, err := img.GetResult(ctx, handle); !errors.Is(err, ErrNotReady) {
		t.Fatalf("early GetResult = %v, want ErrNotReady", err)
	}

	st3, err := img.GetStatus(ctx, handle)
	if err != nil || st3.State != provider.GenerationSucceeded {
		t.Fatalf("status 3 = %+v, %v", st3, err)
	}

	media, err := img.GetResult(ctx, handle)
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	data, readErr := io.ReadAll(media.Body)
	if err := errors.Join(readErr, media.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if media.ContentType != "image/png" || !bytes.Equal(data, samplePNG()) {
		t.Fatalf("media = %q (%d bytes)", media.ContentType, len(data))
	}
	if cdnSawAuthHeader.Load() {
		t.Fatal("Authorization header was leaked to external CDN host")
	}

	if err := img.Cancel(ctx, handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestTextToVideoAndImageToVideoWithRateLimitRetries(t *testing.T) {
	const falKey = "fal_video_key_999"
	fakeMP4 := []byte("\x00\x00\x00\x14ftypisom\x00\x00\x02\x00mp41")

	var submitAttempts atomic.Int32
	var receivedImageURL string

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/fal-ai/kling-video/"):
			attempt := submitAttempts.Add(1)
			if attempt == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
				return
			}
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			receivedImageURL, _ = payload["image_url"].(string)
			_, _ = w.Write([]byte(`{
				"request_id": "req-vid-002",
				"status": "COMPLETED",
				"response_url": "` + server.URL + `/fal-ai/kling-video/requests/req-vid-002"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/fal-ai/kling-video/requests/req-vid-002/status":
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/fal-ai/kling-video/requests/req-vid-002":
			_, _ = w.Write([]byte(`{"video":{"url":"` + server.URL + `/files/clip.mp4","content_type":"video/mp4"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/files/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter, err := newWithHTTPClient(Config{
		APIKey:         falKey,
		BaseURL:        server.URL,
		MaxRetries:     2,
		BaseRetryDelay: 5 * time.Millisecond,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	vid := adapter.Video()

	// 1. Image-to-video (also triggers one 429 retry on Submit).
	handle, err := vid.Submit(ctx, provider.VideoRequest{
		Model:  "fal-ai/kling-video/v1.6/standard/image-to-video",
		Prompt: "Camera pushes slowly through the trees",
		Image:  bytes.NewReader(samplePNG()),
	})
	if err != nil {
		t.Fatalf("Image-to-video Submit: %v", err)
	}
	if !strings.HasPrefix(receivedImageURL, "data:image/png;base64,") {
		t.Fatalf("expected PNG data URI, got %q", receivedImageURL)
	}

	result, err := vid.GetResult(ctx, handle)
	if err != nil {
		t.Fatalf("Video GetResult: %v", err)
	}
	gotMP4, readErr := io.ReadAll(result.Body)
	if err := errors.Join(readErr, result.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "video/mp4" || !bytes.Equal(gotMP4, fakeMP4) {
		t.Fatalf("video output = %q (%d bytes)", result.ContentType, len(gotMP4))
	}

	// 2. Text-to-video (Image == nil omits image_url).
	_, err = vid.Submit(ctx, provider.VideoRequest{
		Model:  "fal-ai/kling-video/v1.6/standard/text-to-video",
		Prompt: "A stormy ocean at night",
	})
	if err != nil {
		t.Fatalf("Text-to-video Submit: %v", err)
	}
	if receivedImageURL != "" {
		t.Fatalf("text-to-video unexpectedly sent image_url: %q", receivedImageURL)
	}

	// 3. Reject non-raster input image for image-to-video.
	_, err = vid.Submit(ctx, provider.VideoRequest{
		Model:  "fal-ai/kling-video/v1.6/standard/image-to-video",
		Prompt: "Invalid input",
		Image:  strings.NewReader("<svg/>"),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SVG image-to-video err = %v, want ErrInvalidRequest", err)
	}
}

func TestValidationSSRFAndUncertainSubmitNoRetry(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty config err = %v", err)
	}
	if _, err := New(Config{APIKey: "k", BaseURL: "https://127.0.0.1"}); !errors.Is(err, ErrBlockedEndpoint) {
		t.Fatalf("loopback SSRF err = %v", err)
	}
	if _, err := New(Config{APIKey: "k", BaseURL: "https://169.254.169.254"}); !errors.Is(err, ErrBlockedEndpoint) {
		t.Fatalf("metadata SSRF err = %v", err)
	}
	if _, err := New(Config{APIKey: "k", Parameters: map[string]any{"prompt": "override"}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("reserved param key err = %v", err)
	}

	badModels := []string{"", "single", "a/../b", "owner/-bad", "a/b/c/d/e/f/g"}
	for _, m := range badModels {
		if err := ValidateModel(m); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("ValidateModel(%q) = %v, want ErrInvalidModel", m, err)
		}
	}

	var postCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream timeout"}`))
	}))
	defer server.Close()

	adapter, err := newWithHTTPClient(Config{
		APIKey:         "fal_secret_xyz",
		BaseURL:        server.URL,
		DefaultModel:   "fal-ai/flux/dev",
		MaxRetries:     3,
		BaseRetryDelay: 5 * time.Millisecond,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = adapter.Image().Submit(context.Background(), provider.ImageRequest{Prompt: "test"})
	if !errors.Is(err, provider.ErrSubmissionUncertain) {
		t.Fatalf("502 Submit err = %v, want ErrSubmissionUncertain", err)
	}
	if postCalls.Load() != 1 {
		t.Fatalf("uncertain POST was retried %d times, want 1", postCalls.Load())
	}
}
