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

// JobConfig defines how temporary worker and render Jobs are constructed.
type JobConfig struct {
	Image                   string
	Claim                   string
	Instance                string
	SharedStorage           bool
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

	if cfg.Image == "" || cfg.Claim == "" || cfg.Instance == "" {
		return JobConfig{}, 0, fmt.Errorf("image, claim and instance are required")
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
		AutomountServiceAccountToken: boolPtr(false),
		SecurityContext:              newPodSecurityContext(),
		Containers: []corev1.Container{
			buildJobContainer(stage, inputPath, outputPath, cfg),
		},
		Volumes: []corev1.Volume{
			{
				Name: dataVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: cfg.Claim,
					},
				},
			},
		},
	}

	if !cfg.SharedStorage {
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
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      dataVolumeName,
				MountPath: dataMountPath,
			},
		},
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

	if cfg.ProviderSecret != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: executor.EnvProviderAPIKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: cfg.ProviderSecret.Name},
					Key:                  cfg.ProviderSecret.Key,
				},
			},
		})
	}

	return container
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
