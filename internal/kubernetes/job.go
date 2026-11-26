// Package kubernetes manages workload Jobs and VideoRun reconciliation.
package kubernetes

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/executor"
	"github.com/tnqbao/kubereel/internal/storage"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	StageWorker = "worker"
	StageRender = "render"

	defaultJobTTLSeconds int32 = 86400
	containerUserID      int64 = 10001
	dataVolumeName             = "data"
	dataMountPath              = "/data"
)

var (
	workflowIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	dnsLabelPattern   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	secretNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	secretKeyPattern  = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

// SecretKeyRef identifies a key inside a namespace-local Kubernetes Secret without exposing its value.
type SecretKeyRef struct {
	Name string
	Key  string
}

// S3SecretRefs identifies static S3 credentials stored in a Kubernetes Secret when workload identity is unavailable.
type S3SecretRefs struct {
	Name               string
	AccessKeyIDKey     string
	SecretAccessKeyKey string
}

// S3JobConfig propagates S3-compatible artifact storage settings to worker and render Jobs.
type S3JobConfig struct {
	Bucket         string
	Region         string
	Prefix         string
	Endpoint       string
	ForcePathStyle bool
	Credentials    *S3SecretRefs
}

// JobConfig defines how temporary worker and render Jobs are constructed.
type JobConfig struct {
	Image                   string
	Claim                   string
	Instance                string
	SharedStorage           bool
	S3                      *S3JobConfig
	WorkerServiceAccount    string
	PullPolicy              corev1.PullPolicy
	TTLSecondsAfterFinished *int32
	Artifacts               *ArtifactConfig
	ProviderName            string
	ProviderSecret          *SecretKeyRef
	ProviderTimeoutSeconds  int64
}

// ArtifactConfig propagates storage bounds to worker and render Jobs.
type ArtifactConfig struct {
	MaxBytes         int64
	RetentionSeconds int64
}

// Job builds a single worker or render Job using the control-plane image.
// With RWO storage, pod affinity keeps workloads on the control-plane node.
func Job(workflowID, stage string, cfg JobConfig) (*batchv1.Job, error) {
	inputPath, outputPath := stageArtifactPaths(workflowID, stage)
	return jobWithPaths(workflowID, stage, inputPath, outputPath, cfg)
}

func jobWithPaths(workflowID, stage, inputPath, outputPath string, cfg JobConfig) (*batchv1.Job, error) {
	normalizedCfg, ttlSeconds, err := validateAndNormalizeJobConfig(workflowID, stage, cfg)
	if err != nil {
		return nil, err
	}

	var backoffLimit int32

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubereel-" + workflowID + "-" + stage,
			Labels: map[string]string{
				"app.kubernetes.io/name": "kubereel",
				"kubereel.io/workflow":   workflowID,
				"kubereel.io/stage":      stage,
				"kubereel.io/instance":   normalizedCfg.Instance,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSeconds,
			Template:                buildJobPodTemplate(workflowID, stage, inputPath, outputPath, normalizedCfg),
		},
	}

	if normalizedCfg.ProviderName != "" {
		job.Labels["kubereel.io/provider"] = normalizedCfg.ProviderName
	}

	if normalizedCfg.ProviderTimeoutSeconds > 0 {
		timeout := normalizedCfg.ProviderTimeoutSeconds
		job.Spec.ActiveDeadlineSeconds = &timeout
	}

	return job, nil
}

func validateAndNormalizeJobConfig(workflowID, stage string, cfg JobConfig) (JobConfig, int32, error) {
	if !workflowIDPattern.MatchString(workflowID) {
		return JobConfig{}, 0, fmt.Errorf("invalid workflow ID")
	}

	if stage != StageWorker && stage != StageRender {
		return JobConfig{}, 0, fmt.Errorf("invalid workflow stage")
	}

	if cfg.Image == "" || cfg.Instance == "" {
		return JobConfig{}, 0, fmt.Errorf("image and instance are required")
	}

	normalizedS3, err := validateJobStorageBinding(cfg.Claim, cfg.S3)
	if err != nil {
		return JobConfig{}, 0, err
	}
	cfg.S3 = normalizedS3

	if !isValidServiceAccountName(cfg.WorkerServiceAccount) {
		return JobConfig{}, 0, fmt.Errorf("invalid worker service account")
	}

	if cfg.PullPolicy == "" {
		cfg.PullPolicy = corev1.PullIfNotPresent
	}

	if !isValidPullPolicy(cfg.PullPolicy) {
		return JobConfig{}, 0, fmt.Errorf("invalid image pull policy")
	}

	if !isValidArtifactConfig(cfg.Artifacts) {
		return JobConfig{}, 0, fmt.Errorf("invalid artifact configuration")
	}

	if !isValidProviderBinding(cfg) {
		return JobConfig{}, 0, fmt.Errorf("invalid provider job configuration")
	}

	ttlSeconds := defaultJobTTLSeconds
	if cfg.TTLSecondsAfterFinished != nil {
		ttlSeconds = *cfg.TTLSecondsAfterFinished
	}

	if ttlSeconds < 0 {
		return JobConfig{}, 0, fmt.Errorf("job retention must not be negative")
	}

	return cfg, ttlSeconds, nil
}

func isValidPullPolicy(policy corev1.PullPolicy) bool {
	return policy == corev1.PullIfNotPresent ||
		policy == corev1.PullAlways ||
		policy == corev1.PullNever
}

func isValidArtifactConfig(cfg *ArtifactConfig) bool {
	if cfg == nil {
		return true
	}

	maxValidRetention := math.MaxInt64 / int64(time.Second)

	return cfg.MaxBytes > 0 &&
		cfg.MaxBytes <= config.MaxArtifactBytesLimit &&
		cfg.RetentionSeconds >= 0 &&
		cfg.RetentionSeconds <= maxValidRetention
}

func isValidProviderBinding(cfg JobConfig) bool {
	if cfg.ProviderName != "" && (len(cfg.ProviderName) > 63 || !dnsLabelPattern.MatchString(cfg.ProviderName)) {
		return false
	}

	if cfg.ProviderTimeoutSeconds < 0 || cfg.ProviderTimeoutSeconds > 86400 {
		return false
	}

	if cfg.ProviderSecret == nil {
		return true
	}

	name := cfg.ProviderSecret.Name
	key := cfg.ProviderSecret.Key
	if len(name) == 0 || len(name) > 253 || !secretNamePattern.MatchString(name) {
		return false
	}

	return len(key) > 0 && len(key) <= 253 && secretKeyPattern.MatchString(key)
}

func validateJobStorageBinding(claim string, s3Cfg *S3JobConfig) (*S3JobConfig, error) {
	if s3Cfg == nil {
		if claim == "" {
			return nil, fmt.Errorf("claim is required for PVC storage")
		}
		return nil, nil
	}

	if err := storage.ValidateS3Bucket(s3Cfg.Bucket); err != nil {
		return nil, err
	}
	if err := storage.ValidateS3Region(s3Cfg.Region); err != nil {
		return nil, err
	}
	if err := storage.ValidateS3Endpoint(s3Cfg.Endpoint); err != nil {
		return nil, err
	}

	prefix, err := storage.NormalizeS3Prefix(s3Cfg.Prefix)
	if err != nil {
		return nil, err
	}
	if !isValidS3SecretRefs(s3Cfg.Credentials) {
		return nil, fmt.Errorf("invalid S3 credentials secret reference")
	}

	normalized := *s3Cfg
	normalized.Prefix = prefix
	return &normalized, nil
}

func isValidS3SecretRefs(refs *S3SecretRefs) bool {
	if refs == nil {
		return true
	}
	if !isValidObjectName(refs.Name) {
		return false
	}
	return isValidSecretKey(refs.AccessKeyIDKey) && isValidSecretKey(refs.SecretAccessKeyKey)
}

func isValidServiceAccountName(name string) bool {
	if name == "" {
		return true
	}
	return isValidObjectName(name)
}

func isValidObjectName(name string) bool {
	return len(name) > 0 && len(name) <= 253 && secretNamePattern.MatchString(name)
}

func isValidSecretKey(key string) bool {
	return len(key) > 0 && len(key) <= 253 && secretKeyPattern.MatchString(key)
}

func stageArtifactPaths(workflowID, stage string) (string, string) {
	workflowDir := path.Join(dataMountPath, "workflows", workflowID)

	if stage == StageRender {
		return path.Join(workflowDir, "render.json"), path.Join(workflowDir, "final.mp4")
	}

	return path.Join(workflowDir, "request.json"), path.Join(workflowDir, "clip.mp4")
}

func buildJobPodTemplate(workflowID, stage, inputPath, outputPath string, cfg JobConfig) corev1.PodTemplateSpec {
	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		ServiceAccountName:           cfg.WorkerServiceAccount,
		AutomountServiceAccountToken: boolPtr(false),
		SecurityContext:              newPodSecurityContext(),
		Containers: []corev1.Container{
			buildJobContainer(stage, inputPath, outputPath, cfg),
		},
	}

	if cfg.S3 == nil {
		podSpec.Volumes = []corev1.Volume{
			{
				Name: dataVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: cfg.Claim,
					},
				},
			},
		}
	}

	if cfg.S3 == nil && !cfg.SharedStorage {
		podSpec.Affinity = controlPlanePodAffinity(cfg.Instance)
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app.kubernetes.io/name": "kubereel",
				"kubereel.io/workflow":   workflowID,
				"kubereel.io/instance":   cfg.Instance,
			},
		},
		Spec: podSpec,
	}
}

func buildJobContainer(stage, inputPath, outputPath string, cfg JobConfig) corev1.Container {
	container := corev1.Container{
		Name:            stage,
		Image:           cfg.Image,
		ImagePullPolicy: cfg.PullPolicy,
		Args:            []string{stage, "--input", inputPath, "--output", outputPath},
		SecurityContext: newContainerSecurityContext(),
		Resources:       defaultJobResources(),
	}

	if cfg.S3 == nil {
		container.VolumeMounts = []corev1.VolumeMount{
			{
				Name:      dataVolumeName,
				MountPath: dataMountPath,
			},
		}
	}

	if cfg.Artifacts != nil {
		container.Env = append(container.Env,
			corev1.EnvVar{
				Name:  config.EnvArtifactMaxBytes,
				Value: strconv.FormatInt(cfg.Artifacts.MaxBytes, 10),
			},
			corev1.EnvVar{
				Name:  config.EnvArtifactRetentionSeconds,
				Value: strconv.FormatInt(cfg.Artifacts.RetentionSeconds, 10),
			},
		)
	}

	if cfg.S3 != nil {
		container.Env = appendS3JobEnv(container.Env, cfg.S3)
	}

	if cfg.ProviderSecret != nil {
		container.Env = append(container.Env, secretEnvVar(executor.EnvProviderAPIKey, cfg.ProviderSecret.Name, cfg.ProviderSecret.Key))
	}

	return container
}

func appendS3JobEnv(env []corev1.EnvVar, s3Cfg *S3JobConfig) []corev1.EnvVar {
	env = append(env,
		corev1.EnvVar{Name: config.EnvStorageType, Value: config.StorageTypeS3},
		corev1.EnvVar{Name: config.EnvS3Bucket, Value: s3Cfg.Bucket},
		corev1.EnvVar{Name: config.EnvS3Region, Value: s3Cfg.Region},
	)

	if s3Cfg.Prefix != "" {
		env = append(env, corev1.EnvVar{Name: config.EnvS3Prefix, Value: s3Cfg.Prefix})
	}
	if s3Cfg.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: config.EnvS3Endpoint, Value: s3Cfg.Endpoint})
	}
	if s3Cfg.ForcePathStyle {
		env = append(env, corev1.EnvVar{Name: config.EnvS3ForcePathStyle, Value: "true"})
	}
	if s3Cfg.Credentials != nil {
		env = append(env,
			secretEnvVar("AWS_ACCESS_KEY_ID", s3Cfg.Credentials.Name, s3Cfg.Credentials.AccessKeyIDKey),
			secretEnvVar("AWS_SECRET_ACCESS_KEY", s3Cfg.Credentials.Name, s3Cfg.Credentials.SecretAccessKeyKey),
		)
	}

	return env
}

func secretEnvVar(envName, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
			},
		},
	}
}

func newPodSecurityContext() *corev1.PodSecurityContext {
	uid := containerUserID

	return &corev1.PodSecurityContext{
		RunAsNonRoot: boolPtr(true),
		RunAsUser:    &uid,
		RunAsGroup:   &uid,
		FSGroup:      &uid,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func newContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func defaultJobResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

func controlPlanePodAffinity(instance string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubereel.io/control-plane": instance,
						},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}
}

func boolPtr(value bool) *bool {
	return &value
}
