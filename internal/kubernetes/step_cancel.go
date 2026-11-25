package kubernetes

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Cancel stops active Jobs and waits for foreground deletion before marking a run cancelled.
func (e *StepExecutor) Cancel(ctx context.Context, run *unstructured.Unstructured) error {
	if e.Runs == nil || e.Jobs == nil || e.JobLock == nil {
		return fmt.Errorf("invalid VideoRun executor configuration")
	}
	e.JobLock.Lock()
	defer e.JobLock.Unlock()
	status, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return fmt.Errorf("read VideoRun status: %w", err)
	}
	if status == nil {
		status = make(map[string]interface{})
	}
	phase, _ := status["phase"].(string)
	if phase == "Succeeded" || phase == "Cancelled" {
		return nil
	}
	steps, err := executableSteps(run)
	if err != nil {
		return err
	}
	statuses, byName, err := stepStatuses(status)
	if err != nil {
		return err
	}
	if (phase == "Failed" || phase == "RequiresReview") && !hasActiveSteps(byName) {
		return nil
	}
	for _, step := range steps {
		if _, exists := byName[step.Name]; !exists {
			entry := map[string]interface{}{"name": step.Name, "phase": "Pending"}
			byName[step.Name] = entry
			statuses = append(statuses, entry)
		}
	}
	if len(byName) != len(steps) {
		return fmt.Errorf("VideoRun status contains unknown steps")
	}
	if phase != "Cancelling" {
		status["phase"] = "Cancelling"
		cancelPending(steps, byName)
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}
	for _, step := range steps {
		entry := byName[step.Name]
		if entry["phase"] != "Running" && entry["phase"] != "Cancelling" {
			continue
		}
		return e.cancelStep(ctx, run, status, statuses, step, entry)
	}
	failed, review := false, false
	for _, entry := range byName {
		if entry["phase"] == "RequiresReview" {
			review = true
		}
		if entry["phase"] == "Failed" {
			failed = true
		}
	}
	status["phase"] = "Cancelled"
	if failed {
		status["phase"] = "Failed"
	}
	if review {
		status["phase"] = "RequiresReview"
	}
	status["steps"] = statuses
	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func hasActiveSteps(steps map[string]map[string]interface{}) bool {
	for _, step := range steps {
		if step["phase"] == "Running" || step["phase"] == "Cancelling" {
			return true
		}
	}
	return false
}

func cancelPending(steps []executableStep, byName map[string]map[string]interface{}) {
	for _, step := range steps {
		entry := byName[step.Name]
		if entry["phase"] != "Pending" {
			continue
		}
		entry["phase"] = "Cancelled"
		entry["ready"] = false
	}
}

func (e *StepExecutor) cancelStep(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}, statuses []interface{}, step executableStep, entry map[string]interface{}) error {
	if step.Type != "image" || step.ProviderRef != "mock" {
		return e.cancelReview(ctx, run, status, statuses, entry, "cannot verify the running step")
	}
	attempt, err := stepAttempt(entry)
	if err != nil {
		return err
	}
	if attempt == 0 {
		return e.cancelReview(ctx, run, status, statuses, entry, "recorded attempt is missing")
	}
	config, err := recordedJobConfig(entry, e.Config)
	if err != nil {
		return e.cancelReview(ctx, run, status, statuses, entry, "recorded Job configuration is invalid")
	}
	expected, err := StepJob(run.GetName(), run.GetUID(), step.Name, attempt, config)
	if err != nil {
		return err
	}
	if err := configureStepJob(expected, step); err != nil {
		return e.cancelReview(ctx, run, status, statuses, entry, "invalid step resource configuration")
	}
	if entry["jobName"] != expected.Name {
		return e.cancelReview(ctx, run, status, statuses, entry, "recorded Job identity is invalid")
	}
	job, err := e.Jobs.Get(ctx, expected.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && entry["deletionRequested"] == true {
		entry["phase"] = "Cancelled"
		entry["ready"] = false
		delete(entry, "deletionRequested")
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}
	if apierrors.IsNotFound(err) {
		return e.cancelReview(ctx, run, status, statuses, entry, "recorded Job is missing")
	}
	if err != nil {
		return fmt.Errorf("get step Job for cancellation: %w", err)
	}
	if err := validateStepJob(job, expected); err != nil {
		return e.cancelReview(ctx, run, status, statuses, entry, err.Error())
	}
	if finished(job, batchv1.JobComplete) {
		delete(entry, "deletionRequested")
		return e.recordSuccess(ctx, run, status, statuses, entry, expected, false)
	}
	if finished(job, batchv1.JobFailed) {
		delete(entry, "deletionRequested")
		entry["phase"] = "Failed"
		entry["ready"] = false
		entry["error"] = "step Job failed"
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}
	if entry["deletionRequested"] == true && job.DeletionTimestamp == nil {
		return e.cancelReview(ctx, run, status, statuses, entry, "Job deletion is not in progress")
	}
	if entry["deletionRequested"] == true {
		return nil
	}
	if job.DeletionTimestamp != nil {
		return e.cancelReview(ctx, run, status, statuses, entry, "Job deletion was not initiated by KubeReel")
	}
	policy := metav1.DeletePropagationForeground
	if err := e.Jobs.Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil {
		return fmt.Errorf("delete step Job: %w", err)
	}
	entry["phase"] = "Cancelling"
	entry["ready"] = false
	entry["deletionRequested"] = true
	status["steps"] = statuses
	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func (e *StepExecutor) cancelReview(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}, statuses []interface{}, entry map[string]interface{}, message string) error {
	entry["phase"] = "RequiresReview"
	entry["ready"] = false
	entry["error"] = message
	status["error"] = message
	status["steps"] = statuses
	return updateVideoRunStatus(ctx, e.Runs, run, status)
}
