package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	maxStepAttempts       int64 = 100
	maxStepTimeoutSeconds int64 = 86400
)

type savedJobConfig struct {
	Image                    string            `json:"image"`
	Claim                    string            `json:"claim"`
	Instance                 string            `json:"instance"`
	SharedStorage            *bool             `json:"sharedStorage"`
	PullPolicy               corev1.PullPolicy `json:"pullPolicy"`
	TTLSeconds               *int32            `json:"ttlSeconds"`
	ArtifactMaxBytes         *int64            `json:"artifactMaxBytes"`
	ArtifactRetentionSeconds *int64            `json:"artifactRetentionSeconds"`
	ProviderName             string            `json:"providerName"`
	SecretName               *string           `json:"secretName"`
	SecretKey                *string           `json:"secretKey"`
	ProviderTimeoutSeconds   *int64            `json:"providerTimeoutSeconds"`
}

func countActiveVideoRunJobs(ctx context.Context, jobs Jobs, instance string) (int, error) {
	selector := "kubereel.io/instance=" + instance + ",kubereel.io/videorun"

	list, err := jobs.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0, fmt.Errorf("list VideoRun jobs: %w", err)
	}

	activeCount := 0
	for i := range list.Items {
		if isJobTerminal(&list.Items[i]) {
			continue
		}
		activeCount++
	}

	return activeCount, nil
}

func countActiveProviderJobs(ctx context.Context, jobs Jobs, instance, providerName string) (int, error) {
	selector := "kubereel.io/instance=" + instance + ",kubereel.io/provider=" + providerName

	list, err := jobs.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0, fmt.Errorf("list provider jobs: %w", err)
	}

	activeCount := 0
	for i := range list.Items {
		if isJobTerminal(&list.Items[i]) {
			continue
		}
		activeCount++
	}

	return activeCount, nil
}

func jobConfigStatus(cfg JobConfig, job *batchv1.Job) map[string]interface{} {
	status := map[string]interface{}{
		"image":         cfg.Image,
		"claim":         cfg.Claim,
		"instance":      cfg.Instance,
		"sharedStorage": cfg.SharedStorage,
		"pullPolicy":    string(job.Spec.Template.Spec.Containers[0].ImagePullPolicy),
		"ttlSeconds":    int64(*job.Spec.TTLSecondsAfterFinished),
	}

	if cfg.Artifacts != nil {
		status["artifactMaxBytes"] = cfg.Artifacts.MaxBytes
		status["artifactRetentionSeconds"] = cfg.Artifacts.RetentionSeconds
	}
	if cfg.ProviderName != "" {
		status["providerName"] = cfg.ProviderName
	}
	if cfg.ProviderSecret != nil {
		status["secretName"] = cfg.ProviderSecret.Name
		status["secretKey"] = cfg.ProviderSecret.Key
	}
	if cfg.ProviderTimeoutSeconds > 0 {
		status["providerTimeoutSeconds"] = cfg.ProviderTimeoutSeconds
	}

	return status
}

func recordedJobConfig(stepEntry map[string]interface{}, fallback JobConfig) (JobConfig, error) {
	stored, found, err := unstructured.NestedMap(stepEntry, "jobConfig")
	if err != nil {
		return JobConfig{}, fmt.Errorf("read recorded Job configuration: %w", err)
	}
	if !found {
		return fallback, nil
	}

	saved, err := decodeSavedJobConfig(stored)
	if err != nil {
		return JobConfig{}, err
	}

	return buildJobConfigFromSaved(saved)
}

func decodeSavedJobConfig(stored map[string]interface{}) (savedJobConfig, error) {
	data, err := json.Marshal(stored)
	if err != nil {
		return savedJobConfig{}, fmt.Errorf("encode recorded Job configuration: %w", err)
	}

	var saved savedJobConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		return savedJobConfig{}, fmt.Errorf("decode recorded Job configuration: %w", err)
	}

	return saved, nil
}

func buildJobConfigFromSaved(saved savedJobConfig) (JobConfig, error) {
	if saved.Image == "" || saved.Claim == "" || saved.Instance == "" || saved.SharedStorage == nil || saved.TTLSeconds == nil {
		return JobConfig{}, fmt.Errorf("recorded Job configuration is incomplete")
	}

	if *saved.TTLSeconds < 0 || !isValidPullPolicy(saved.PullPolicy) {
		return JobConfig{}, fmt.Errorf("recorded Job configuration is invalid")
	}

	if (saved.ArtifactMaxBytes == nil) != (saved.ArtifactRetentionSeconds == nil) {
		return JobConfig{}, fmt.Errorf("recorded artifact configuration is incomplete")
	}

	if (saved.SecretName == nil) != (saved.SecretKey == nil) {
		return JobConfig{}, fmt.Errorf("recorded secret configuration is incomplete")
	}

	cfg := JobConfig{
		Image:                   saved.Image,
		Claim:                   saved.Claim,
		Instance:                saved.Instance,
		SharedStorage:           *saved.SharedStorage,
		PullPolicy:              saved.PullPolicy,
		TTLSecondsAfterFinished: saved.TTLSeconds,
		ProviderName:            saved.ProviderName,
	}

	if saved.ArtifactMaxBytes != nil {
		cfg.Artifacts = &ArtifactConfig{
			MaxBytes:         *saved.ArtifactMaxBytes,
			RetentionSeconds: *saved.ArtifactRetentionSeconds,
		}
	}

	if saved.SecretName != nil {
		cfg.ProviderSecret = &SecretKeyRef{
			Name: *saved.SecretName,
			Key:  *saved.SecretKey,
		}
	}

	if saved.ProviderTimeoutSeconds != nil {
		cfg.ProviderTimeoutSeconds = *saved.ProviderTimeoutSeconds
	}

	if !isValidArtifactConfig(cfg.Artifacts) || !isValidProviderBinding(cfg) {
		return JobConfig{}, fmt.Errorf("recorded job configuration is invalid")
	}

	return cfg, nil
}

// StepJob builds a deterministic worker Job owned by a VideoRun.
func StepJob(runName string, uid types.UID, stepName string, attempt int64, cfg JobConfig) (*batchv1.Job, error) {
	return buildOwnedStepJob(runName, uid, stepName, attempt, StageWorker, "request.json", "scene.svg", cfg)
}

// StepRenderJob builds a deterministic FFmpeg render Job owned by a VideoRun.
func StepRenderJob(runName string, uid types.UID, stepName string, attempt int64, cfg JobConfig) (*batchv1.Job, error) {
	return buildOwnedStepJob(runName, uid, stepName, attempt, StageRender, "render.json", "output.mp4", cfg)
}

func stepJobForStep(runName string, uid types.UID, step executableStep, attempt int64, cfg JobConfig) (*batchv1.Job, error) {
	if step.Type == "ffmpeg" {
		return StepRenderJob(runName, uid, step.Name, attempt, cfg)
	}

	return buildOwnedStepJob(
		runName,
		uid,
		step.Name,
		attempt,
		StageWorker,
		"request.json",
		stepOutputFile(step.Type),
		cfg,
	)
}

func stepOutputArtifactKey(step executableStep, workflowID string) string {
	return path.Join("workflows", workflowID, stepOutputFile(step.Type))
}

func stepOutputFile(stepType string) string {
	switch stepType {
	case "llm":
		return "script.json"
	case "video":
		return "clip.mp4"
	case "ffmpeg":
		return "output.mp4"
	default:
		return "scene.svg"
	}
}

func buildOwnedStepJob(
	runName string,
	uid types.UID,
	stepName string,
	attempt int64,
	stage, inputFile, outputFile string,
	cfg JobConfig,
) (*batchv1.Job, error) {
	if runName == "" || uid == "" || stepName == "" {
		return nil, fmt.Errorf("run name, UID and step name are required")
	}

	if attempt < 1 || attempt > maxStepAttempts {
		return nil, fmt.Errorf("attempt must be between 1 and %d", maxStepAttempts)
	}

	workflowID := deterministicStepID(uid, stepName, attempt)
	workflowDir := path.Join(dataMountPath, "workflows", workflowID)

	job, err := jobWithPaths(workflowID, stage, path.Join(workflowDir, inputFile), path.Join(workflowDir, outputFile), cfg)
	if err != nil {
		return nil, err
	}

	job.Labels["kubereel.io/videorun"] = string(uid)
	job.Annotations = map[string]string{"kubereel.io/step": stepName}
	job.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "kubereel.io/v1alpha1",
			Kind:       "VideoRun",
			Name:       runName,
			UID:        uid,
			Controller: boolPtr(true),
		},
	}

	return job, nil
}

func deterministicStepID(uid types.UID, stepName string, attempt int64) string {
	identity := string(uid) + "\x00" + stepName
	if attempt > 1 {
		identity = fmt.Sprintf("%s\x00%d", identity, attempt)
	}

	hash := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(hash[:16])
}

func validateStepJob(actual, expected *batchv1.Job) error {
	if err := validateJobIdentity(actual, expected); err != nil {
		return err
	}

	if !apiequality.Semantic.DeepEqual(actual.OwnerReferences, expected.OwnerReferences) ||
		actual.Annotations["kubereel.io/step"] != expected.Annotations["kubereel.io/step"] {
		return fmt.Errorf("step Job ownership mismatch")
	}

	if !hasMatchingJobSpecTimers(&actual.Spec, &expected.Spec) {
		return fmt.Errorf("step Job execution configuration mismatch")
	}

	if err := validateStepPodSpec(&actual.Spec.Template.Spec, &expected.Spec.Template.Spec); err != nil {
		return err
	}

	return validateStepContainerSpec(
		&actual.Spec.Template.Spec.Containers[0],
		&expected.Spec.Template.Spec.Containers[0],
	)
}

func hasMatchingJobSpecTimers(actual, expected *batchv1.JobSpec) bool {
	return apiequality.Semantic.DeepEqual(actual.ActiveDeadlineSeconds, expected.ActiveDeadlineSeconds) &&
		apiequality.Semantic.DeepEqual(actual.BackoffLimit, expected.BackoffLimit) &&
		apiequality.Semantic.DeepEqual(actual.TTLSecondsAfterFinished, expected.TTLSecondsAfterFinished)
}

func validateStepPodSpec(actual, expected *corev1.PodSpec) error {
	hasUnexpectedPodFeatures := len(actual.InitContainers) != 0 ||
		len(actual.EphemeralContainers) != 0 ||
		actual.HostNetwork ||
		actual.HostPID ||
		actual.HostIPC
	if hasUnexpectedPodFeatures {
		return fmt.Errorf("step Job pod configuration mismatch")
	}

	matchesExpectedPod := apiequality.Semantic.DeepEqual(actual.Volumes, expected.Volumes) &&
		apiequality.Semantic.DeepEqual(actual.SecurityContext, expected.SecurityContext) &&
		apiequality.Semantic.DeepEqual(actual.AutomountServiceAccountToken, expected.AutomountServiceAccountToken) &&
		apiequality.Semantic.DeepEqual(actual.Affinity, expected.Affinity)
	if !matchesExpectedPod {
		return fmt.Errorf("step Job pod configuration mismatch")
	}

	return nil
}

func validateStepContainerSpec(actual, expected *corev1.Container) error {
	hasUnexpectedRuntimeHooks := len(actual.Command) != 0 ||
		len(actual.EnvFrom) != 0 ||
		actual.WorkingDir != "" ||
		actual.Lifecycle != nil ||
		actual.LivenessProbe != nil ||
		actual.ReadinessProbe != nil ||
		actual.StartupProbe != nil ||
		len(actual.Ports) != 0 ||
		actual.Stdin ||
		actual.TTY
	if hasUnexpectedRuntimeHooks {
		return fmt.Errorf("step Job container configuration mismatch")
	}

	matchesExpectedContainer := actual.ImagePullPolicy == expected.ImagePullPolicy &&
		apiequality.Semantic.DeepEqual(actual.Env, expected.Env) &&
		apiequality.Semantic.DeepEqual(actual.SecurityContext, expected.SecurityContext) &&
		apiequality.Semantic.DeepEqual(actual.VolumeMounts, expected.VolumeMounts) &&
		apiequality.Semantic.DeepEqual(actual.Resources, expected.Resources)
	if !matchesExpectedContainer {
		return fmt.Errorf("step Job container configuration mismatch")
	}

	return nil
}

func configureStepJob(job *batchv1.Job, step executableStep) error {
	if step.TimeoutSeconds < 0 || step.TimeoutSeconds > maxStepTimeoutSeconds {
		return fmt.Errorf("invalid step timeout")
	}

	if step.TimeoutSeconds > 0 {
		job.Spec.ActiveDeadlineSeconds = &step.TimeoutSeconds
	}

	resources := &job.Spec.Template.Spec.Containers[0].Resources
	if err := applyResourceValues(resources.Requests, step.Resources.Requests); err != nil {
		return err
	}

	if err := applyResourceValues(resources.Limits, step.Resources.Limits); err != nil {
		return err
	}

	for _, resourceName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		reqQuantity := resources.Requests[resourceName]
		limitQuantity := resources.Limits[resourceName]
		if reqQuantity.Cmp(limitQuantity) > 0 {
			return fmt.Errorf("resource request exceeds limit")
		}
	}

	return nil
}

func applyResourceValues(target corev1.ResourceList, rawValues map[string]string) error {
	for name, rawValue := range rawValues {
		if name != "cpu" && name != "memory" {
			return fmt.Errorf("unsupported resource")
		}

		quantity, err := resource.ParseQuantity(rawValue)
		if err != nil {
			return fmt.Errorf("invalid resource quantity: %w", err)
		}

		if quantity.Sign() <= 0 {
			return fmt.Errorf("resource quantity must be positive")
		}

		target[corev1.ResourceName(name)] = quantity
	}

	return nil
}
