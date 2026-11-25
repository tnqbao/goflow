package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsScrapeIncludesWorkerSummaryAndBoundedLabels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer upstream.Close()
	ctx, recorder := WithProviderRecorder(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := DoProviderRequest(upstream.Client(), req, "fal")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	raw, err := json.Marshal(recorder.samples)
	if err != nil {
		t.Fatal(err)
	}

	metrics := NewMetrics()
	metrics.SetInventory(func() (int, int) { return 2, 3 })
	metrics.SetLegacyActive(1)
	metrics.RunFinished("Succeeded", 2*time.Second)
	metrics.RunFinished("Failed", time.Second)
	metrics.StepFinished("Succeeded", time.Second)
	metrics.StepRetried()
	metrics.ArtifactsPublished(2)
	metrics.RecordProviderSummary(string(raw))
	metrics.RecordProviderSummary(`{"unbounded-label":{"count":1,"seconds":1}}`)
	result := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{
		"kubereel_active_executions 3",
		"kubereel_active_jobs 3",
		"kubereel_executions_completed_total 1",
		"kubereel_executions_failed_total 1",
		`kubereel_execution_duration_seconds_count{outcome="Succeeded"} 1`,
		`kubereel_step_duration_seconds_count{outcome="Succeeded"} 1`,
		"kubereel_step_retries_total 1",
		"kubereel_artifacts_generated_total 2",
		`kubereel_provider_errors_total{provider="fal"} 1`,
		`kubereel_provider_request_duration_seconds_count{provider="fal"} 1`,
	} {
		if !strings.Contains(result.Body.String(), line) {
			t.Errorf("missing metric %q", line)
		}
	}
	if result.Code != http.StatusOK || strings.Contains(result.Body.String(), "unbounded-label") {
		t.Fatal("unexpected metric response")
	}
}
