package observability

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sync"
	"time"
)

const terminationPath = "/dev/termination-log"
const maxSummaryBytes = 4096

var providerBuckets = [...]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

type ProviderSample struct {
	Count   uint64                       `json:"count"`
	Errors  uint64                       `json:"errors"`
	Seconds float64                      `json:"seconds"`
	Buckets [len(providerBuckets)]uint64 `json:"buckets"`
}

type providerRecorderKey struct{}

type ProviderRecorder struct {
	mu      sync.Mutex
	samples map[string]ProviderSample
}

func WithProviderRecorder(ctx context.Context) (context.Context, *ProviderRecorder) {
	recorder := &ProviderRecorder{samples: make(map[string]ProviderSample)}
	return context.WithValue(ctx, providerRecorderKey{}, recorder), recorder
}

// DoProviderRequest measures an individual HTTP attempt through receipt of headers.
func DoProviderRequest(client *http.Client, req *http.Request, provider string) (*http.Response, error) {
	start := time.Now()
	response, err := client.Do(req)
	recorder, _ := req.Context().Value(providerRecorderKey{}).(*ProviderRecorder)
	if recorder != nil && knownProvider(provider) {
		failed := err != nil || response.StatusCode < 200 || response.StatusCode >= 300
		recorder.observe(provider, time.Since(start).Seconds(), failed)
	}
	return response, err
}

func knownProvider(provider string) bool {
	return provider == "gemini" || provider == "fal"
}

func (r *ProviderRecorder) observe(provider string, elapsed float64, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sample := r.samples[provider]
	sample.Count++
	sample.Seconds += elapsed
	if failed {
		sample.Errors++
	}
	for i, bound := range providerBuckets {
		if elapsed <= bound {
			sample.Buckets[i]++
		}
	}
	r.samples[provider] = sample
}

// WriteTerminationSummary writes a bounded summary to the file mounted by Kubernetes.
func (r *ProviderRecorder) WriteTerminationSummary() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == 0 {
		return nil
	}
	file, err := os.OpenFile(terminationPath, os.O_WRONLY|os.O_TRUNC, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeSummary(file, r.samples)
}

func writeSummary(file *os.File, samples map[string]ProviderSample) error {
	writeErr := json.NewEncoder(file).Encode(samples)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func decodeProviderSummary(raw string) map[string]ProviderSample {
	if len(raw) > maxSummaryBytes {
		return nil
	}
	var samples map[string]ProviderSample
	if err := json.Unmarshal([]byte(raw), &samples); err != nil {
		return nil
	}
	for provider, sample := range samples {
		if !knownProvider(provider) || !sample.valid() {
			delete(samples, provider)
		}
	}
	return samples
}

func (s ProviderSample) valid() bool {
	if s.Count == 0 || s.Count > 1_000_000 || s.Errors > s.Count || math.IsInf(s.Seconds, 0) || math.IsNaN(s.Seconds) || s.Seconds < 0 {
		return false
	}
	var last uint64
	for _, bucket := range s.Buckets {
		if bucket < last || bucket > s.Count {
			return false
		}
		last = bucket
	}
	return true
}
