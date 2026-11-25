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

	"github.com/tnqbao/kubereel/internal/executor"
	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider/fal"
	"github.com/tnqbao/kubereel/internal/provider/gemini"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
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
	Providers     dynamic.ResourceInterface
	Jobs          Jobs
	Workflows     *service.WorkflowService
	Config        JobConfig
	Artifacts     storage.Store
	MaxConcurrent int
	JobLock       *sync.Mutex
}

type stepExecutionPlan struct {
	payload     []byte
	inputFile   string
	jobOverride JobConfig
}

type executableStep struct {
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	DependsOn      []string          `json:"dependsOn"`
	ProviderRef    string            `json:"providerRef"`
	Model          string            `json:"model"`
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
		if !e.isSupportedStep(step) {
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

		plan, blockedByProviderLimit, invalidReason, err := e.prepareStepPlan(ctx, run, step, stepsByName, byName)
		if err != nil {
			return err
		}
		if invalidReason != "" {
			return e.setRunAndStepState(ctx, run, status, statuses, entry, "Failed", invalidReason)
		}
		if blockedByProviderLimit {
			continue
		}

		return e.submitStep(ctx, run, status, statuses, step, entry, plan)
	}

	return nil
}

func (e *StepExecutor) validateConfig() error {
	if e.Runs == nil || e.Jobs == nil || e.Workflows == nil || e.JobLock == nil || e.Artifacts == nil || e.MaxConcurrent < 1 {
		return fmt.Errorf("invalid VideoRun executor configuration")
	}

	return nil
}

func isSupportedProviderStepType(stepType string) bool {
	return stepType == mock.StepLLM || stepType == mock.StepImage || stepType == mock.StepVideo
}

func isSupportedMockProviderStep(step executableStep) bool {
	if step.ProviderRef != "mock" {
		return false
	}

	return isSupportedProviderStepType(step.Type)
}

func (e *StepExecutor) isSupportedStep(step executableStep) bool {
	if isSupportedMockProviderStep(step) || step.Type == "ffmpeg" {
		return true
	}

	return e.Providers != nil && step.ProviderRef != "" && isSupportedProviderStepType(step.Type)
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

func (e *StepExecutor) prepareStepPlan(
	ctx context.Context,
	run *unstructured.Unstructured,
	step executableStep,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) (stepExecutionPlan, bool, string, error) {
	if step.Type == "ffmpeg" {
		payload, inputFile, invalidReason, err := prepareFFmpegPayload(run.GetUID(), step, stepsByName, statusesByName)
		return stepExecutionPlan{payload: payload, inputFile: inputFile, jobOverride: e.Config}, false, invalidReason, err
	}

	prompt, valid, err := readMockPrompt(run)
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	if !valid {
		return stepExecutionPlan{}, false, "step requires a nonempty prompt or topic of at most 8192 bytes", nil
	}

	if step.ProviderRef == "mock" {
		return e.buildMockStepPlan(step, prompt)
	}

	return e.prepareExternalProviderPlan(ctx, run.GetUID(), step, prompt, stepsByName, statusesByName)
}

func (e *StepExecutor) prepareExternalProviderPlan(
	ctx context.Context,
	uid types.UID,
	step executableStep,
	prompt string,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) (stepExecutionPlan, bool, string, error) {
	vp, err := e.Providers.Get(ctx, step.ProviderRef, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return stepExecutionPlan{}, false, fmt.Sprintf("VideoProvider %q not found", step.ProviderRef), nil
	}
	if err != nil {
		return stepExecutionPlan{}, false, "", fmt.Errorf("get VideoProvider %q: %w", step.ProviderRef, err)
	}

	enabled, configured, err := unstructured.NestedBool(vp.Object, "spec", "enabled")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	if configured && !enabled {
		return stepExecutionPlan{}, false, "provider is disabled", nil
	}

	providerType, _, err := unstructured.NestedString(vp.Object, "spec", "type")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}

	switch providerType {
	case "mock":
		capability := step.Type
		if capability == mock.StepLLM {
			capability = "text"
		}
		capabilities, _, err := unstructured.NestedStringSlice(vp.Object, "spec", "capabilities")
		if err != nil {
			return stepExecutionPlan{}, false, "", err
		}
		if !slices.Contains(capabilities, capability) {
			return stepExecutionPlan{}, false, "provider does not declare the required capability", nil
		}
		return e.buildMockStepPlan(step, prompt)

	case executor.ProviderGemini:
		if step.Type != mock.StepLLM {
			return stepExecutionPlan{}, false, fmt.Sprintf("provider type %q is not supported for %s steps", providerType, step.Type), nil
		}
		return e.buildGeminiStepPlan(ctx, step, vp, prompt)
	case executor.ProviderFal:
		if step.Type != mock.StepImage && step.Type != mock.StepVideo {
			return stepExecutionPlan{}, false, fmt.Sprintf("provider type %q is not supported for %s steps", providerType, step.Type), nil
		}
		return e.buildFalStepPlan(ctx, uid, step, vp, prompt, stepsByName, statusesByName)
	default:
		return stepExecutionPlan{}, false, fmt.Sprintf("provider type %q is not supported for %s steps", providerType, step.Type), nil
	}
}

func (e *StepExecutor) buildMockStepPlan(step executableStep, prompt string) (stepExecutionPlan, bool, string, error) {
	payload, err := json.Marshal(mock.Request{Type: step.Type, Prompt: prompt})
	if err != nil {
		return stepExecutionPlan{}, false, "", fmt.Errorf("encode mock request: %w", err)
	}
	return stepExecutionPlan{payload: payload, inputFile: "request.json", jobOverride: e.Config}, false, "", nil
}

func (e *StepExecutor) buildGeminiStepPlan(
	ctx context.Context,
	step executableStep,
	vp *unstructured.Unstructured,
	prompt string,
) (stepExecutionPlan, bool, string, error) {
	capabilities, _, err := unstructured.NestedStringSlice(vp.Object, "spec", "capabilities")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	if !slices.Contains(capabilities, "text") {
		return stepExecutionPlan{}, false, fmt.Sprintf("VideoProvider %q does not declare text capability", step.ProviderRef), nil
	}

	secretRef, secretReason := readProviderSecretRef(vp, step.ProviderRef)
	if secretReason != "" {
		return stepExecutionPlan{}, false, secretReason, nil
	}

	baseURL, _, _ := unstructured.NestedString(vp.Object, "spec", "baseURL")
	if endpointReason := validateOptionalGeminiBaseURL(baseURL); endpointReason != "" {
		return stepExecutionPlan{}, false, endpointReason, nil
	}

	models, _, _ := unstructured.NestedStringSlice(vp.Object, "spec", "models")
	selectedModel, modelReason := selectProviderModel(step.Model, models, gemini.DefaultModel)
	if modelReason != "" {
		return stepExecutionPlan{}, false, modelReason, nil
	}
	if modelErr := gemini.ValidateModel(selectedModel); modelErr != nil {
		return stepExecutionPlan{}, false, modelErr.Error(), nil
	}

	maxConc, hasMaxConc, err := unstructured.NestedInt64(vp.Object, "spec", "maxConcurrent")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	atCapacity, err := e.isProviderAtCapacity(ctx, step.ProviderRef, hasMaxConc, maxConc)
	if err != nil || atCapacity {
		return stepExecutionPlan{}, atCapacity, "", err
	}

	vpTimeout, _, _ := unstructured.NestedInt64(vp.Object, "spec", "timeoutSeconds")
	jobCfg := e.providerJobConfig(step, secretRef, vpTimeout)

	payload, err := json.Marshal(executor.StepRequest{
		Provider:       executor.ProviderGemini,
		Type:           mock.StepLLM,
		Model:          selectedModel,
		BaseURL:        baseURL,
		TimeoutSeconds: vpTimeout,
		Prompt:         prompt,
	})
	if err != nil {
		return stepExecutionPlan{}, false, "", fmt.Errorf("encode gemini step request: %w", err)
	}

	return stepExecutionPlan{
		payload:     payload,
		inputFile:   "request.json",
		jobOverride: jobCfg,
	}, false, "", nil
}

func (e *StepExecutor) buildFalStepPlan(
	ctx context.Context,
	uid types.UID,
	step executableStep,
	vp *unstructured.Unstructured,
	prompt string,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) (stepExecutionPlan, bool, string, error) {
	capabilities, _, err := unstructured.NestedStringSlice(vp.Object, "spec", "capabilities")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	if !slices.Contains(capabilities, step.Type) {
		return stepExecutionPlan{}, false, fmt.Sprintf("VideoProvider %q does not declare %s capability", step.ProviderRef, step.Type), nil
	}

	secretRef, secretReason := readProviderSecretRef(vp, step.ProviderRef)
	if secretReason != "" {
		return stepExecutionPlan{}, false, secretReason, nil
	}

	baseURL, _, _ := unstructured.NestedString(vp.Object, "spec", "baseURL")
	if endpointReason := validateOptionalFalBaseURL(baseURL); endpointReason != "" {
		return stepExecutionPlan{}, false, endpointReason, nil
	}

	models, _, _ := unstructured.NestedStringSlice(vp.Object, "spec", "models")
	selectedModel, modelReason := selectProviderModel(step.Model, models, "")
	if modelReason != "" {
		return stepExecutionPlan{}, false, modelReason, nil
	}
	if modelErr := fal.ValidateModel(selectedModel); modelErr != nil {
		return stepExecutionPlan{}, false, modelErr.Error(), nil
	}

	imageInput, depReason, err := resolveFalImageDependency(uid, step, stepsByName, statusesByName)
	if err != nil || depReason != "" {
		return stepExecutionPlan{}, false, depReason, err
	}

	maxConc, hasMaxConc, err := unstructured.NestedInt64(vp.Object, "spec", "maxConcurrent")
	if err != nil {
		return stepExecutionPlan{}, false, "", err
	}
	atCapacity, err := e.isProviderAtCapacity(ctx, step.ProviderRef, hasMaxConc, maxConc)
	if err != nil || atCapacity {
		return stepExecutionPlan{}, atCapacity, "", err
	}

	vpTimeout, _, _ := unstructured.NestedInt64(vp.Object, "spec", "timeoutSeconds")
	jobCfg := e.providerJobConfig(step, secretRef, vpTimeout)

	payload, err := json.Marshal(executor.StepRequest{
		Provider:       executor.ProviderFal,
		Type:           step.Type,
		Model:          selectedModel,
		BaseURL:        baseURL,
		ImageInput:     imageInput,
		TimeoutSeconds: vpTimeout,
		Prompt:         prompt,
	})
	if err != nil {
		return stepExecutionPlan{}, false, "", fmt.Errorf("encode fal step request: %w", err)
	}

	return stepExecutionPlan{
		payload:     payload,
		inputFile:   "request.json",
		jobOverride: jobCfg,
	}, false, "", nil
}

func readProviderSecretRef(vp *unstructured.Unstructured, providerRef string) (*SecretKeyRef, string) {
	secretName, _, _ := unstructured.NestedString(vp.Object, "spec", "secretRef", "name")
	secretKey, _, _ := unstructured.NestedString(vp.Object, "spec", "secretRef", "key")
	if secretName == "" || secretKey == "" {
		return nil, fmt.Sprintf("VideoProvider %q is missing secretRef", providerRef)
	}

	return &SecretKeyRef{Name: secretName, Key: secretKey}, ""
}

func (e *StepExecutor) providerJobConfig(step executableStep, secretRef *SecretKeyRef, vpTimeout int64) JobConfig {
	jobCfg := e.Config
	jobCfg.ProviderName = step.ProviderRef
	jobCfg.ProviderSecret = secretRef
	if step.TimeoutSeconds == 0 && vpTimeout > 0 {
		jobCfg.ProviderTimeoutSeconds = vpTimeout
	}

	return jobCfg
}

func selectProviderModel(stepModel string, providerModels []string, defaultModel string) (string, string) {
	if stepModel != "" && len(providerModels) > 0 && !slices.Contains(providerModels, stepModel) {
		return "", fmt.Sprintf("step model %q is not listed in VideoProvider models", stepModel)
	}
	if stepModel != "" {
		return stepModel, ""
	}
	if len(providerModels) > 0 && providerModels[0] != "" {
		return providerModels[0], ""
	}
	if defaultModel != "" {
		return defaultModel, ""
	}

	return "", "step model or VideoProvider models entry is required"
}

func resolveFalImageDependency(
	uid types.UID,
	step executableStep,
	stepsByName map[string]executableStep,
	statusesByName map[string]map[string]interface{},
) (string, string, error) {
	if step.Type != mock.StepVideo {
		return "", "", nil
	}

	var imageInput string
	for _, depName := range step.DependsOn {
		depStep, exists := stepsByName[depName]
		if !exists || depStep.Type != mock.StepImage {
			continue
		}
		if imageInput != "" {
			return "", "fal video step supports at most one upstream image dependency", nil
		}

		depStatus := statusesByName[depName]
		key, reason, err := resolveCompletedRasterDependency(uid, depName, depStep, depStatus)
		if err != nil || reason != "" {
			return "", reason, err
		}
		imageInput = key
	}

	return imageInput, "", nil
}

func resolveCompletedRasterDependency(
	uid types.UID,
	depName string,
	depStep executableStep,
	depStatus map[string]interface{},
) (string, string, error) {
	if depStatus == nil || depStatus["phase"] != "Succeeded" {
		return "", fmt.Sprintf("dependency step %q is not completed", depName), nil
	}
	if !hasRecordedExternalProvider(depStatus) {
		return "", "fal video step requires a raster image dependency; mock image steps output SVG", nil
	}

	attempt, err := stepAttempt(depStatus)
	if err != nil {
		return "", "", err
	}
	if attempt < 1 {
		return "", fmt.Sprintf("dependency step %q has no recorded output", depName), nil
	}

	depWorkflowID := deterministicStepID(uid, depStep.Name, attempt)
	return stepOutputArtifactKey(depStep, depWorkflowID, true), "", nil
}

func hasRecordedExternalProvider(stepEntry map[string]interface{}) bool {
	providerName, _, err := unstructured.NestedString(stepEntry, "jobConfig", "providerName")
	return err == nil && providerName != ""
}

func validateOptionalGeminiBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}

	if err := gemini.ValidateEndpoint(baseURL); err != nil {
		return err.Error()
	}

	return ""
}

func validateOptionalFalBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}

	if err := fal.ValidateEndpoint(baseURL); err != nil {
		return err.Error()
	}

	return ""
}

func (e *StepExecutor) isProviderAtCapacity(ctx context.Context, providerName string, hasMax bool, maxConc int64) (bool, error) {
	if !hasMax || maxConc <= 0 {
		return false, nil
	}

	activeCount, err := countActiveProviderJobs(ctx, e.Jobs, e.Config.Instance, providerName)
	if err != nil {
		return false, err
	}

	return int64(activeCount) >= maxConc, nil
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
		rawInputs = defaultFFmpegDependencies(step.DependsOn, stepsByName)
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
	return stepOutputArtifactKey(depStep, depWorkflowID, hasRecordedExternalProvider(depStatus)), "", nil
}

func defaultFFmpegDependencies(dependsOn []string, stepsByName map[string]executableStep) []string {
	mediaDeps := make([]string, 0, len(dependsOn))
	for _, depName := range dependsOn {
		dep := stepsByName[depName]
		if dep.Type == mock.StepVideo || dep.Type == "ffmpeg" {
			mediaDeps = append(mediaDeps, depName)
		}
	}

	if len(mediaDeps) > 0 {
		return mediaDeps
	}

	return dependsOn
}

func readMockPrompt(run *unstructured.Unstructured) (string, bool, error) {
	prompt, found, err := unstructured.NestedString(run.Object, "spec", "inputs", "prompt", "stringValue")
	if err != nil {
		return "", false, fmt.Errorf("read mock prompt: %w", err)
	}
	if found {
		trimmed := strings.TrimSpace(prompt)
		return prompt, trimmed != "" && len(trimmed) <= maxPromptBytes, nil
	}

	topic, hasTopic, err := unstructured.NestedString(run.Object, "spec", "inputs", "topic", "stringValue")
	if err != nil {
		return "", false, fmt.Errorf("read workflow topic: %w", err)
	}
	if !hasTopic {
		return "", false, nil
	}

	combined := strings.TrimSpace(topic)
	if combined == "" {
		return "", false, nil
	}

	style, hasStyle, err := unstructured.NestedString(run.Object, "spec", "inputs", "style", "stringValue")
	if err != nil {
		return "", false, fmt.Errorf("read workflow style: %w", err)
	}
	if hasStyle && strings.TrimSpace(style) != "" {
		combined = combined + " (" + strings.TrimSpace(style) + ")"
	}

	if len(combined) > maxPromptBytes {
		return "", false, nil
	}

	return combined, true, nil
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
	plan stepExecutionPlan,
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

	jobCfg, err := recordedJobConfig(entry, plan.jobOverride)
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
	if err := publishStepPayload(ctx, e.Artifacts, workflowID, plan.inputFile, plan.payload); err != nil {
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
	activeWorkflows, err := e.Workflows.Active(ctx)
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
		workflowID := expectedJob.Labels["kubereel.io/workflow"]
		uncertain, uncertainErr := executor.HasUncertainOutcome(ctx, e.Artifacts, workflowID)
		if uncertainErr != nil {
			return false, uncertainErr
		}
		if uncertain {
			return true, e.setRunAndStepState(ctx, run, status, statuses, entry, "RequiresReview", "provider submission outcome is uncertain")
		}
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
	artifactKey := stepOutputArtifactKey(step, workflowID, job.Labels["kubereel.io/provider"] != "")

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
	switch step.Type {
	case mock.StepLLM:
		return mock.ValidateScript(ctx, artifacts, artifactKey)
	case mock.StepVideo, "ffmpeg":
		return renderer.ValidateMP4(ctx, artifacts, artifactKey)
	default:
		return executor.ValidateImageArtifact(ctx, artifacts, artifactKey)
	}
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
