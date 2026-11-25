package kubernetes

import (
	"context"
	"log/slog"
	"time"

	"github.com/tnqbao/kubereel/internal/observability"
	"github.com/tnqbao/kubereel/internal/repository"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

type runObserverKey struct{}

type runObserver struct {
	metrics    *observability.Metrics
	repository *repository.ExecutionRepository
}

func (o runObserver) installInventory(runs, jobs cache.SharedIndexInformer) {
	if o.metrics == nil {
		return
	}
	o.metrics.SetInventory(func() (activeRuns, activeJobs int) {
		for _, raw := range runs.GetStore().List() {
			run, ok := raw.(*unstructured.Unstructured)
			if !ok || run.GetDeletionTimestamp() != nil {
				continue
			}
			status, _, _ := unstructured.NestedMap(run.Object, "status")
			phase, _ := status["phase"].(string)
			switch phase {
			case "", "Pending", "Running", "Cancelling":
				activeRuns++
			case "Failed", "RequiresReview":
				_, steps, _ := stepStatuses(status)
				if hasActiveSteps(steps) {
					activeRuns++
				}
			}
		}
		if jobs == nil {
			return activeRuns, 0
		}
		for _, raw := range jobs.GetStore().List() {
			job, ok := raw.(*batchv1.Job)
			if ok && !isJobTerminal(job) {
				activeJobs++
			}
		}
		return activeRuns, activeJobs
	})
}

func stampRunStatus(previous, next map[string]any, now time.Time) {
	stamp := now.UTC().Format(time.RFC3339Nano)
	if next["phase"] == "Running" && next["startedAt"] == nil {
		next["startedAt"] = stamp
	}
	_, steps, _ := stepStatuses(next)
	if terminalRunStatus(next, steps) && next["completedAt"] == nil {
		next["completedAt"] = stamp
	}
	_, oldSteps, _ := stepStatuses(previous)
	for _, raw := range statusEntries(next) {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := step["name"].(string)
		prior := oldSteps[name]
		if step["phase"] == "Running" && prior["phase"] != "Running" {
			step["startedAt"] = stamp
			delete(step, "completedAt")
		}
		if attemptFinished(prior, step) {
			step["completedAt"] = stamp
		}
	}
}

func statusEntries(status map[string]any) []any {
	entries, _ := status["steps"].([]any)
	return entries
}

func terminalRunStatus(status map[string]any, steps map[string]map[string]any) bool {
	switch status["phase"] {
	case "Succeeded", "Failed", "Cancelled", "RequiresReview":
		return !hasActiveSteps(steps)
	default:
		return false
	}
}

func attemptFinished(previous, next map[string]any) bool {
	if previous["phase"] != "Running" && previous["phase"] != "Cancelling" {
		return false
	}
	switch next["phase"] {
	case "Pending", "Succeeded", "Failed", "Cancelled", "RequiresReview":
		return true
	default:
		return false
	}
}

func (o runObserver) record(ctx context.Context, run *unstructured.Unstructured, previous, next map[string]any) {
	workflowID, _, _ := unstructured.NestedString(run.Object, "spec", "workflowRef")
	logger := slog.With("workflow_id", workflowID, "run_id", run.GetName())
	oldArtifacts, _, _ := unstructured.NestedStringSlice(previous, "artifacts")
	newArtifacts, _, _ := unstructured.NestedStringSlice(next, "artifacts")
	if o.metrics != nil && len(newArtifacts) > len(oldArtifacts) {
		o.metrics.ArtifactsPublished(len(newArtifacts) - len(oldArtifacts))
	}
	if previous["phase"] != next["phase"] {
		logger.InfoContext(ctx, "execution state changed", "phase", next["phase"])
	}
	if o.metrics != nil && previous["completedAt"] == nil && next["completedAt"] != nil {
		phase, _ := next["phase"].(string)
		o.metrics.RunFinished(phase, statusElapsed(next, run.GetCreationTimestamp().Time))
	}
	_, priorSteps, _ := stepStatuses(previous)
	_, afterSteps, _ := stepStatuses(next)
	for name, step := range afterSteps {
		o.recordStep(ctx, run, logger, name, priorSteps[name], step)
	}
}

func (o runObserver) recordStep(ctx context.Context, run *unstructured.Unstructured, logger *slog.Logger, name string, prior, step map[string]any) {
	if prior["phase"] == step["phase"] && prior["attempts"] == step["attempts"] {
		return
	}
	job, _ := step["jobName"].(string)
	if job == "" {
		job, _ = prior["jobName"].(string)
	}
	logger.InfoContext(ctx, "execution step state changed", "step_id", name, "job_id", job, "phase", step["phase"], "attempt", step["attempts"])
	if o.metrics == nil {
		return
	}
	attempt, _ := step["attempts"].(int64)
	if attempt > 1 && prior["attempts"] != step["attempts"] {
		o.metrics.StepRetried()
	}
	if !attemptFinished(prior, step) {
		return
	}
	phase, _ := step["phase"].(string)
	o.metrics.StepFinished(phase, statusElapsed(step, time.Time{}))
	provider, _, _ := unstructured.NestedString(prior, "jobConfig", "providerName")
	if provider == "" || job == "" || o.repository == nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	summary, err := o.repository.ProviderSummary(readCtx, run, name, job)
	if err != nil {
		logger.WarnContext(ctx, "provider metrics unavailable", "step_id", name, "job_id", job)
		return
	}
	o.metrics.RecordProviderSummary(summary)
}

func statusElapsed(status map[string]any, fallback time.Time) time.Duration {
	startRaw, _ := status["startedAt"].(string)
	endRaw, _ := status["completedAt"].(string)
	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		start = fallback
	}
	end, err := time.Parse(time.RFC3339Nano, endRaw)
	if err != nil || start.IsZero() {
		return -1
	}
	return end.Sub(start)
}
