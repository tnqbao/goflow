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

	statuses, err = populateMissingStepStatuses(steps, statuses, byName)
	if err != nil {
		return err
	}

	if phase != "Cancelling" {
		status["phase"] = "Cancelling"
		cancelPendingSteps(steps, byName)
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}

	for _, step := range steps {
		entry := byName[step.Name]
		if entry["phase"] != "Running" && entry["phase"] != "Cancelling" {
			continue
		}

		return e.cancelActiveStep(ctx, run, status, statuses, step, entry)
	}

	status["phase"] = resolveTerminalCancelledPhase(byName)
	status["steps"] = statuses

	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func populateMissingStepStatuses(
	steps []executableStep,
	statuses []interface{},
	byName map[string]map[string]interface{},
) ([]interface{}, error) {
	for _, step := range steps {
		if _, exists := byName[step.Name]; exists {
			continue
		}

		entry := map[string]interface{}{
			"name":  step.Name,
			"phase": "Pending",
		}
		byName[step.Name] = entry
		statuses = append(statuses, entry)
	}

	if len(byName) != len(steps) {
		return nil, fmt.Errorf("VideoRun status contains unknown steps")
	}

	return statuses, nil
}

func resolveTerminalCancelledPhase(byName map[string]map[string]interface{}) string {
	hasFailed := false
	for _, entry := range byName {
		if entry["phase"] == "RequiresReview" {
			return "RequiresReview"
		}
		if entry["phase"] == "Failed" {
			hasFailed = true
		}
	}

	if hasFailed {
		return "Failed"
	}

	return "Cancelled"
}

func hasActiveSteps(byName map[string]map[string]interface{}) bool {
	for _, entry := range byName {
		if entry["phase"] == "Running" || entry["phase"] == "Cancelling" {
			return true
		}
	}

	return false
}

func cancelPendingSteps(steps []executableStep, byName map[string]map[string]interface{}) {
	for _, step := range steps {
		entry := byName[step.Name]
		if entry["phase"] != "Pending" {
			continue
		}

		entry["phase"] = "Cancelled"
		entry["ready"] = false
	}
}

func (e *StepExecutor) cancelActiveStep(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	step executableStep,
	entry map[string]interface{},
) error {
	expectedJob, reviewReason, err := e.verifyStepForCancellation(run, step, entry)
	if err != nil {
		return err
	}
	if reviewReason != "" {
		return e.markStepRequiresReview(ctx, run, status, statuses, entry, reviewReason)
	}

	actualJob, err := e.Jobs.Get(ctx, expectedJob.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return e.handleMissingJobDuringCancel(ctx, run, status, statuses, entry)
	}
	if err != nil {
		return fmt.Errorf("get step Job for cancellation: %w", err)
	}

	if err := validateStepJob(actualJob, expectedJob); err != nil {
		return e.markStepRequiresReview(ctx, run, status, statuses, entry, err.Error())
	}

	if hasJobCondition(actualJob, batchv1.JobComplete) {
		delete(entry, "deletionRequested")
		return e.recordStepSuccess(ctx, run, status, statuses, step, entry, expectedJob, false)
	}

	if hasJobCondition(actualJob, batchv1.JobFailed) {
		delete(entry, "deletionRequested")
		entry["phase"] = "Failed"
		entry["ready"] = false
		entry["error"] = "step Job failed"
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}

	return e.requestOrAwaitJobDeletion(ctx, run, status, statuses, entry, actualJob)
}

func (e *StepExecutor) verifyStepForCancellation(
	run *unstructured.Unstructured,
	step executableStep,
	entry map[string]interface{},
) (*batchv1.Job, string, error) {
	if !isSupportedExecutableStep(step) {
		return nil, "cannot verify the running step", nil
	}

	attempt, err := stepAttempt(entry)
	if err != nil {
		return nil, "", err
	}
	if attempt == 0 {
		return nil, "recorded attempt is missing", nil
	}

	jobCfg, err := recordedJobConfig(entry, e.Config)
	if err != nil {
		return nil, "recorded Job configuration is invalid", nil
	}

	expectedJob, err := stepJobForStep(run.GetName(), run.GetUID(), step, attempt, jobCfg)
	if err != nil {
		return nil, "", err
	}

	if err := configureStepJob(expectedJob, step); err != nil {
		return nil, "invalid step resource configuration", nil
	}

	if entry["jobName"] != expectedJob.Name {
		return nil, "recorded Job identity is invalid", nil
	}

	return expectedJob, "", nil
}

func (e *StepExecutor) handleMissingJobDuringCancel(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	entry map[string]interface{},
) error {
	if entry["deletionRequested"] != true {
		return e.markStepRequiresReview(ctx, run, status, statuses, entry, "recorded Job is missing")
	}

	entry["phase"] = "Cancelled"
	entry["ready"] = false
	delete(entry, "deletionRequested")
	status["steps"] = statuses

	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func (e *StepExecutor) requestOrAwaitJobDeletion(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	entry map[string]interface{},
	job *batchv1.Job,
) error {
	if entry["deletionRequested"] == true {
		if job.DeletionTimestamp == nil {
			return e.markStepRequiresReview(ctx, run, status, statuses, entry, "Job deletion is not in progress")
		}
		return nil
	}

	if job.DeletionTimestamp != nil {
		return e.markStepRequiresReview(ctx, run, status, statuses, entry, "Job deletion was not initiated by KubeReel")
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

func (e *StepExecutor) markStepRequiresReview(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	entry map[string]interface{},
	message string,
) error {
	entry["phase"] = "RequiresReview"
	entry["ready"] = false
	entry["error"] = message

	status["error"] = message
	status["steps"] = statuses

	return updateVideoRunStatus(ctx, e.Runs, run, status)
}
