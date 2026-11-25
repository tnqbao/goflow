package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
)

func TestGenerateInteractionsAPI(t *testing.T) {
	const testKey = "test-gemini-secret-key-12345"

	var receivedPath, receivedHeader, receivedModel, receivedInput string
	var receivedStore bool

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedHeader = r.Header.Get("x-goog-api-key")

		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}

		var payload struct {
			Model string `json:"model"`
			Input string `json:"input"`
			Store bool   `json:"store"`
		}
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		receivedModel = payload.Model
		receivedInput = payload.Input
		receivedStore = payload.Store

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "v1_interaction_abc",
			"object": "interaction",
			"status": "completed",
			"steps": [
				{"type": "thought", "content": [{"type": "text", "text": "internal reasoning"}]},
				{"type": "model_output", "content": [{"type": "text", "text": "Scene 1: Foggy forest.\n"}]},
				{"type": "model_output", "content": [{"type": "text", "text": "Scene 2: Distant footsteps."}]}
			]
		}`))
	}))
	defer server.Close()

	adapter, err := newWithHTTPClient(Config{
		APIKey:  testKey,
		BaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	res, err := adapter.Generate(context.Background(), provider.TextRequest{
		Prompt: "  Write a two-scene horror short script.  ",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if receivedPath != "/v1/interactions" {
		t.Fatalf("path = %q, want /v1/interactions", receivedPath)
	}
	if receivedHeader != testKey {
		t.Fatalf("x-goog-api-key = %q, want %q", receivedHeader, testKey)
	}
	if receivedModel != DefaultModel || receivedStore {
		t.Fatalf("model=%q store=%v", receivedModel, receivedStore)
	}
	if receivedInput != "Write a two-scene horror short script." {
		t.Fatalf("input = %q", receivedInput)
	}
	if res.Text != "Scene 1: Foggy forest.\nScene 2: Distant footsteps." {
		t.Fatalf("output text = %q", res.Text)
	}
}

func TestConfigModelAndSSRFValidation(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing key err = %v", err)
	}

	badEndpoints := []struct {
		url     string
		wantErr error
	}{
		{"http://generativelanguage.googleapis.com", ErrInvalidConfig},
		{"https://user:pass@generativelanguage.googleapis.com", ErrInvalidConfig},
		{"https://generativelanguage.googleapis.com?key=secret", ErrInvalidConfig},
		{"https://127.0.0.1:8443", ErrBlockedEndpoint},
		{"https://10.0.0.5", ErrBlockedEndpoint},
		{"https://169.254.169.254", ErrBlockedEndpoint},
		{"https://[::1]:8443", ErrBlockedEndpoint},
	}
	for _, tc := range badEndpoints {
		if _, err := New(Config{APIKey: "key", BaseURL: tc.url}); !errors.Is(err, tc.wantErr) {
			t.Fatalf("New(%q) = %v, want %v", tc.url, err, tc.wantErr)
		}
	}

	deprecatedModels := []string{"gemini-1.5-pro", "gemini-2.0-flash", "gemini-2.5-pro", "../bad"}
	for _, model := range deprecatedModels {
		if err := ValidateModel(model); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("ValidateModel(%q) = %v, want ErrInvalidModel", model, err)
		}
	}

	validAdapter, err := New(Config{APIKey: "key", DefaultModel: "gemini-4-neon"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validAdapter.Generate(context.Background(), provider.TextRequest{Model: "gemini-2.5-flash", Prompt: "hi"}); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("deprecated request model err = %v", err)
	}
}

func TestErrorClassificationSecretScrubbingAndUncertainSubmission(t *testing.T) {
	const secretKey = "AIzaSySecretDoNotLeak999"

	statusToTrigger := http.StatusUnauthorized
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusToTrigger == http.StatusFound {
			http.Redirect(w, r, "https://example.com/steal", http.StatusFound)
			return
		}
		if statusToTrigger == http.StatusGatewayTimeout {
			time.Sleep(80 * time.Millisecond)
		}
		w.WriteHeader(statusToTrigger)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad key ` + secretKey + `"}}`))
	}))
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return ErrRedirectDisallowed
	}

	adapter, err := newWithHTTPClient(Config{APIKey: secretKey, BaseURL: server.URL}, client)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := adapter.Generate(context.Background(), provider.TextRequest{Prompt: "hello"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("401 err = %v, want ErrUnauthorized", err)
	}

	statusToTrigger = http.StatusBadRequest
	_, err = adapter.Generate(context.Background(), provider.TextRequest{Prompt: "hello"})
	if !errors.Is(err, ErrInvalidRequest) || strings.Contains(err.Error(), secretKey) {
		t.Fatalf("400 err = %v (leaked secret or wrong error)", err)
	}

	statusToTrigger = http.StatusTooManyRequests
	if _, err := adapter.Generate(context.Background(), provider.TextRequest{Prompt: "hello"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 err = %v, want ErrRateLimited", err)
	}

	statusToTrigger = http.StatusFound
	if _, err := adapter.Generate(context.Background(), provider.TextRequest{Prompt: "hello"}); !errors.Is(err, ErrRedirectDisallowed) {
		t.Fatalf("redirect err = %v, want ErrRedirectDisallowed", err)
	}

	statusToTrigger = http.StatusServiceUnavailable
	if _, err := adapter.Generate(context.Background(), provider.TextRequest{Prompt: "hello"}); !errors.Is(err, provider.ErrSubmissionUncertain) {
		t.Fatalf("503 err = %v, want ErrSubmissionUncertain", err)
	}

	statusToTrigger = http.StatusGatewayTimeout
	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := adapter.Generate(shortCtx, provider.TextRequest{Prompt: "hello"}); !errors.Is(err, provider.ErrSubmissionUncertain) {
		t.Fatalf("mid-flight timeout err = %v, want ErrSubmissionUncertain", err)
	}
}
