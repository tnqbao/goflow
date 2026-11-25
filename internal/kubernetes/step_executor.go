package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const maxPromptBytes = 8192

// StepExecutor executes supported VideoRun steps using Kubernetes Jobs.
type StepExecutor struct {
	Runs          dynamic.ResourceInterface
	Jobs          Jobs
	Store         *workflow.Store
	Config        JobConfig
	Artifacts     storage.Store
	MaxConcurrent int
	JobLock       *sync.Mutex
}

type executableStep struct {
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	DependsOn      []string          `json:"dependsOn"`
	ProviderRef    string            `json:"providerRef"`
	FFmpeg         *renderer.Request `json:"ffmpeg"`
	TimeoutSeconds int64             `json:"timeoutSeconds"`
	Retry          struct {
		MaxAttempts int64 `json:"maxAttempts"`
	} `json:"retry"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// Reconcile submits or observes one supported step at a time.
func (e *StepExecutor) Reconcile(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
) error {
	if err := e.validateConfig(); err != nil {
		return err
	}

	e.JobLock.Lock()
	defer e.JobLock.Unlock()

	steps, err := executableSteps(run)
	if err != nil {
		return err
	}

	stepsByName := indexExecutableSteps(steps)

	statuses, byName, err := stepStatuses(status)
	if err != nil {
		return err
	}

	stepLimit, hasStepLimit, err := unstructured.NestedInt64(run.Object, "spec", "execution", "maxConcurrentSteps")
	if err != nil {
		return fmt.Errorf("read step concurrency limit: %w", err)
	}

	runningSteps := countRunningSteps(byName)

	for _, step := range steps {
		if !isSupportedExecutableStep(step) {
			continue
		}

		entry := byName[step.Name]
		if entry == nil {
			return fmt.Errorf("missing status for step %q", step.Name)
		}

		switch entry["phase"] {
		case "Running":
			changed, err := e.observeRunningStep(ctx, run, status, statuses, step, entry)
			if err != nil || changed {
				return err
			}
			continue
		case "Pending":
		default:
			continue
		}

		if entry["ready"] != true {
			continue
		}
		if hasStepLimit && runningSteps >= stepLimit {
			continue
		}

		payload, inputFile, invalidReason, err := prepareStepPayload(run, step, stepsByName, byName)
		if err != nil {
			return err
		}
		if invalidReason != "" {
			return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", invalidReason)
		}

		return e.submitStep(ctx, run, status, statuses, step, entry, inputFile, payload)
	}

	return nil
}

func (e *StepExecutor) validateConfig() error {
	if e.Runs == nil || e.Jobs == nil || e.Store == nil || e.JobLock == nil || e.Artifacts == nil || e.MaxConcurrent < 1 {
		return fmt.Errorf("invalid VideoRun executor configuration")
	}

	return nil
}

func isSupportedMockImageStep(step executableStep) bool {
	return step.Type == "image" && step.ProviderRef == "mock"
}

func isSupportedExecutableStep(step executableStep) bool {
	return isSupportedMockImageStep(step) || step.Type == "ffmpeg"
}

func indexExecutableSteps(steps []executableStep) map[string]executableStep {
	indexed := make(map[string]executableStep, len(steps))
	for _, step := range steps {
		indexed[step.Name] = step
	}

	return indexed
}

func countRunningSteps(byName map[string]map[string]interface{}) int64 {
	var count int64
	for _, entry := range byName {
		if entry["phase"] == "Running" {
			count++
		}
	}

	return count
}

func prepareStepPayload(
	run *unstructured.Unstructured,
	step executableStep,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) ([]byte, string, string, error) {
	if step.Type == "ffmpeg" {
		return prepareFFmpegPayload(run.GetUID(), step, stepsByName, statusesByName)
	}

	prompt, valid, err := readMockPrompt(run)
	if err != nil {
		return nil, "", "", err
	}
	if !valid {
		return nil, "", "mock image requires a nonempty prompt of at most 8192 bytes", nil
	}

	payload, err := json.Marshal(mock.Request{Prompt: prompt})
	if err != nil {
		return nil, "", "", fmt.Errorf("encode mock request: %w", err)
	}

	return payload, "request.json", "", nil
}

func prepareFFmpegPayload(
	uid types.UID,
	step executableStep,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) ([]byte, string, string, error) {
	if step.ProviderRef != "" {
		return nil, "", "ffmpeg step must not set providerRef", nil
	}
	if step.FFmpeg == nil {
		return nil, "", "ffmpeg step requires ffmpeg configuration", nil
	}

	req := *step.FFmpeg
	rawInputs := req.Inputs
	if len(rawInputs) == 0 && len(step.DependsOn) > 0 {
		rawInputs = step.DependsOn
	}

	resolvedInputs, invalidReason, err := resolveFFmpegInputs(uid, step, rawInputs, stepsByName, statusesByName)
	if err != nil || invalidReason != "" {
		return nil, "", invalidReason, err
	}
	req.Inputs = resolvedInputs

	if req.AudioInput != "" {
		audioKey, audioReason, audioErr := resolveFFmpegReference(uid, step, req.AudioInput, stepsByName, statusesByName)
		if audioErr != nil || audioReason != "" {
			return nil, "", audioReason, audioErr
		}
		req.AudioInput = audioKey
	}

	if err := renderer.ValidateRequest(req); err != nil {
		return nil, "", err.Error(), nil
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("encode ffmpeg request: %w", err)
	}

	return payload, "render.json", "", nil
}

func resolveFFmpegInputs(
	uid types.UID,
	step executableStep,
	rawInputs []string,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) ([]string, string, error) {
	resolved := make([]string, 0, len(rawInputs))
	for _, item := range rawInputs {
		key, invalidReason, err := resolveFFmpegReference(uid, step, item, stepsByName, statusesByName)
		if err != nil || invalidReason != "" {
			return nil, invalidReason, err
		}
		resolved = append(resolved, key)
	}

	return resolved, "", nil
}

func resolveFFmpegReference(
	uid types.UID,
	step executableStep,
	ref string,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) (string, string, error) {
	if !slices.Contains(step.DependsOn, ref) {
		return ref, "", nil
	}

	depStep, exists := stepsByName[ref]
	depStatus := statusesByName[ref]
	if !exists || depStatus == nil || depStatus["phase"] != "Succeeded" {
		return "", fmt.Sprintf("dependency step %q is not completed", ref), nil
	}

	attempt, err := stepAttempt(depStatus)
	if err != nil {
		return "", "", err
	}
	if attempt < 1 {
		return "", fmt.Sprintf("dependency step %q has no recorded output", ref), nil
	}

	depWorkflowID := deterministicStepID(uid, depStep.Name, attempt)
	return stepOutputArtifactKey(depStep, depWorkflowID), "", nil
}

func readMockPrompt(run *unstructured.Unstructured) (string, bool, error) {
	prompt, found, err := unstructured.NestedString(run.Object, "spec", "inputs", "prompt", "stringValue")
	if err != nil {
		return "", false, fmt.Errorf("read mock prompt: %w", err)
	}

	trimmed := strings.TrimSpace(prompt)
	if !found || trimmed == "" || len(trimmed) > maxPromptBytes {
		return "", false, nil
	}

	return prompt, true, nil
}

func executableSteps(run *unstructured.Unstructured) ([]executableStep, error) {
	snapshotMap, found, err := unstructured.NestedMap(run.Object, "spec", "workflowSnapshot")
	if err != nil {
		return nil, fmt.Errorf("read workflow snapshot: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("workflow snapshot is missing")
	}

	data, err := json.Marshal(snapshotMap)
	if err != nil {
		return nil, fmt.Errorf("encode workflow snapshot: %w", err)
	}

	var snapshot struct {
		Steps []executableStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode workflow snapshot: %w", err)
	}

	return snapshot.Steps, nil
}

func stepStatuses(status map[string]interface{}) ([]interface{}, map[string]map[string]interface{}, error) {
	statuses, _, err := unstructured.NestedSlice(status, "steps")
	if err != nil {
		return nil, nil, fmt.Errorf("read step statuses: %w", err)
	}

	byName := make(map[string]map[string]interface{}, len(statuses))
	for _, rawItem := range statuses {
		stepMap, ok := rawItem.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("invalid step status")
		}

		name, ok := stepMap["name"].(string)
		if !ok {
			return nil, nil, fmt.Errorf("invalid step status name")
		}

		byName[name] = stepMap
	}

	return statuses, byName, nil
}

func (e *StepExecutor) submitStep(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	step executableStep,
	entry map[string]interface{},
	inputFile string,
	payload []byte,
) error {
	available, err := e.hasAvailableJobSlot(ctx)
	if err != nil || !available {
		return err
	}

	attempt, err := stepAttempt(entry)
	if err != nil {
		return err
	}
	if attempt >= maxAttemptsForStep(step) {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", "step retry limit exhausted")
	}

	_, hasRecordedConfig := entry["jobConfig"]
	if attempt > 0 && !hasRecordedConfig {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "retry has no recorded Job configuration")
	}

	jobCfg, err := recordedJobConfig(entry, e.Config)
	if err != nil {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job configuration is invalid")
	}

	job, err := stepJobForStep(run.GetName(), run.GetUID(), step, attempt+1, jobCfg)
	if err != nil {
		return err
	}
	if err := configureStepJob(job, step); err != nil {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", "invalid step resource configuration")
	}

	_, err = e.Jobs.Get(ctx, job.Name, metav1.GetOptions{})
	if err == nil {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "a Job exists without a recorded submission")
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check step Job: %w", err)
	}

	workflowID := job.Labels["kubereel.io/workflow"]
	if err := publishStepPayload(ctx, e.Artifacts, workflowID, inputFile, payload); err != nil {
		return fmt.Errorf("write step request: %w", err)
	}

	recordStepSubmission(status, statuses, entry, jobCfg, job, attempt+1)
	if err := updateVideoRunStatus(ctx, e.Runs, run, status); err != nil {
		return err
	}

	if _, err := e.Jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("submit step Job: %w", err)
	}

	return nil
}

func publishStepPayload(ctx context.Context, artifacts storage.Store, workflowID, inputFile string, payload []byte) error {
	if !workflowIDPattern.MatchString(workflowID) {
		return fmt.Errorf("invalid workflow ID")
	}

	requestKey := path.Join("workflows", workflowID, inputFile)
	return artifacts.Put(ctx, requestKey, bytes.NewReader(payload))
}

func (e *StepExecutor) hasAvailableJobSlot(ctx context.Context) (bool, error) {
	activeWorkflows, err := e.Store.Active(ctx)
	if err != nil {
		return false, err
	}

	activeVideoJobs, err := countActiveVideoRunJobs(ctx, e.Jobs, e.Config.Instance)
	if err != nil {
		return false, err
	}

	totalActive := activeVideoJobs + countRunningWorkflows(activeWorkflows)
	return totalActive < e.MaxConcurrent, nil
}

func recordStepSubmission(
	status map[string]interface{},
	statuses []interface{},
	entry map[string]interface{},
	jobCfg JobConfig,
	job *batchv1.Job,
	nextAttempt int64,
) {
	entry["phase"] = "Running"
	entry["ready"] = false
	entry["jobName"] = job.Name
	entry["jobConfig"] = jobConfigStatus(jobCfg, job)
	entry["attempts"] = nextAttempt
	delete(entry, "error")

	status["phase"] = "Running"
	status["steps"] = statuses
}

func (e *StepExecutor) observeRunningStep(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	step executableStep,
	entry map[string]interface{},
) (bool, error) {
	attempt, err := stepAttempt(entry)
	if err != nil {
		return false, err
	}
	if attempt == 0 {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "recorded attempt is missing")
	}

	jobCfg, err := recordedJobConfig(entry, e.Config)
	if err != nil {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job configuration is invalid")
	}

	expectedJob, err := stepJobForStep(run.GetName(), run.GetUID(), step, attempt, jobCfg)
	if err != nil {
		return false, err
	}
	if err := configureStepJob(expectedJob, step); err != nil {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", "invalid step resource configuration")
	}

	if entry["jobName"] != expectedJob.Name {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job identity is invalid")
	}

	actualJob, err := e.Jobs.Get(ctx, expectedJob.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job is missing")
	}
	if err != nil {
		return false, fmt.Errorf("get step Job: %w", err)
	}

	if err := validateStepJob(actualJob, expectedJob); err != nil {
		return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", err.Error())
	}

	if hasJobCondition(actualJob, batchv1.JobFailed) {
		entry["jobConfig"] = jobConfigStatus(jobCfg, expectedJob)
		return true, e.retryOrFailStep(ctx, run, status, statuses, step, entry, attempt)
	}

	if !hasJobCondition(actualJob, batchv1.JobComplete) {
		return false, nil
	}

	return true, e.recordStepSuccess(ctx, run, status, statuses, step, entry, expectedJob, true)
}

func (e *StepExecutor) recordStepSuccess(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	step executableStep,
	entry map[string]interface{},
	job *batchv1.Job,
	finishRun bool,
) error {
	workflowID := job.Labels["kubereel.io/workflow"]
	artifactKey := stepOutputArtifactKey(step, workflowID)

	err := validateStepArtifact(ctx, e.Artifacts, step, artifactKey)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil && !finishRun {
		entry["phase"] = "Failed"
		entry["ready"] = false
		entry["error"] = "step artifact is missing or invalid"
		status["steps"] = statuses
		return updateVideoRunStatus(ctx, e.Runs, run, status)
	}
	if err != nil {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", "step artifact is missing or invalid")
	}

	entry["phase"] = "Succeeded"
	entry["ready"] = false
	status["steps"] = statuses

	if err := appendRunArtifact(status, artifactKey); err != nil {
		return err
	}

	if finishRun && allStepsSucceeded(statuses) {
		finalizeCompletedRun(run, status, statuses)
	}

	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func validateStepArtifact(ctx context.Context, artifacts storage.Store, step executableStep, artifactKey string) error {
	if step.Type == "ffmpeg" {
		return renderer.ValidateMP4(ctx, artifacts, artifactKey)
	}

	return mock.ValidateSVG(ctx, artifacts, artifactKey)
}

func appendRunArtifact(status map[string]interface{}, artifactPath string) error {
	artifacts, _, err := unstructured.NestedStringSlice(status, "artifacts")
	if err != nil {
		return fmt.Errorf("read run artifacts: %w", err)
	}

	if !slices.Contains(artifacts, artifactPath) {
		artifacts = append(artifacts, artifactPath)
	}

	return unstructured.SetNestedStringSlice(status, artifacts, "artifacts")
}

func finalizeCompletedRun(
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
) {
	status["phase"] = "Succeeded"
	status["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := validateRunOutputs(run, statuses); err != nil {
		status["phase"] = "Failed"
		status["error"] = err.Error()
	}
}

func stepAttempt(entry map[string]interface{}) (int64, error) {
	attempt, found, err := unstructured.NestedInt64(entry, "attempts")
	if err != nil {
		return 0, fmt.Errorf("read step attempts: %w", err)
	}
	if !found {
		return 0, nil
	}

	if attempt < 0 || attempt > maxStepAttempts {
		return 0, fmt.Errorf("invalid step attempts")
	}

	return attempt, nil
}

func (e *StepExecutor) retryOrFailStep(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	step executableStep,
	entry map[string]interface{},
	attempt int64,
) error {
	if attempt >= maxAttemptsForStep(step) {
		return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", "step Job failed")
	}

	entry["phase"] = "Pending"
	entry["ready"] = true
	entry["error"] = "previous step Job failed"
	delete(entry, "jobName")

	status["steps"] = statuses
	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func maxAttemptsForStep(step executableStep) int64 {
	if step.Retry.MaxAttempts == 0 {
		return 1
	}

	return step.Retry.MaxAttempts
}

func validateRunOutputs(run *unstructured.Unstructured, statuses []interface{}) error {
	outputs, _, err := unstructured.NestedSlice(run.Object, "spec", "workflowSnapshot", "outputs")
	if err != nil {
		return fmt.Errorf("read workflow outputs: %w", err)
	}

	succeededByName := make(map[string]bool, len(statuses))
	for _, rawItem := range statuses {
		stepMap, ok := rawItem.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid step status")
		}

		name, ok := stepMap["name"].(string)
		if !ok {
			return fmt.Errorf("invalid step status name")
		}

		succeededByName[name] = stepMap["phase"] == "Succeeded"
	}

	for _, rawOutput := range outputs {
		outputMap, ok := rawOutput.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid workflow output")
		}

		sourceStep, ok := outputMap["from"].(string)
		if !ok || !succeededByName[sourceStep] {
			return fmt.Errorf("workflow output references an incomplete or unknown step")
		}
	}

	return nil
}

func (e *StepExecutor) setRunAndStepState(
	ctx context.Context,
	run *unstructured.Unstructured,
	status map[string]interface{},
	statuses []interface{},
	entry map[string]interface{},
	phase, message string,
) error {
	entry["phase"] = phase
	entry["ready"] = false
	entry["error"] = message

	status["steps"] = statuses
	status["phase"] = phase
	status["error"] = message

	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func allStepsSucceeded(statuses []interface{}) bool {
	for _, rawItem := range statuses {
		stepMap, ok := rawItem.(map[string]interface{})
		if !ok || stepMap["phase"] != "Succeeded" {
			return false
		}
	}

	return len(statuses) > 0
}
