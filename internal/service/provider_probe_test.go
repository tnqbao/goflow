package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestProviderProbeReadsOfficialMetadata(t *testing.T) {
	for _, tt := range []struct{ kind, host, path, header, value, check string }{
		{"gemini", "generativelanguage.googleapis.com", "/v1beta/models", "x-goog-api-key", "test-key", "model_catalog"},
		{"fal", "api.fal.ai", "/v1/models/pricing", "Authorization", "Key test-key", "model_pricing"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			client := &http.Client{Transport: probeTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.Host != tt.host || req.URL.Path != tt.path || req.Header.Get(tt.header) != tt.value {
					t.Fatal("unexpected provider metadata request")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			})}
			check, err := probeProvider(context.Background(), client, tt.kind, "test-key", []string{"fal-ai/example"})
			if err != nil || check.Status != "ok" || check.Check != tt.check {
				t.Fatalf("check = %+v, %v", check, err)
			}
		})
	}
}
