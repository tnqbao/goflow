// Package kubernetes defines workload Jobs for the control plane.
package kubernetes

import (
	"fmt"
	"path"
	"regexp"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var workflowID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type JobConfig struct {
	Image         string
	Claim         string
	Instance      string
	SharedStorage bool
	PullPolicy    corev1.PullPolicy
}

// Job builds a single worker or render Job using the control-plane image.
// With RWO storage, pod affinity keeps workloads on the control-plane node.
func Job(id, stage string, cfg JobConfig) (*batchv1.Job, error) {
	if !workflowID.MatchString(id) {
		return nil, fmt.Errorf("invalid workflow ID")
	}
	if stage != "worker" && stage != "render" {
		return nil, fmt.Errorf("invalid workflow stage")
	}
	if cfg.Image == "" || cfg.Claim == "" || cfg.Instance == "" {
		return nil, fmt.Errorf("image, claim and instance are required")
	}
	if cfg.PullPolicy == "" {
		cfg.PullPolicy = corev1.PullIfNotPresent
	}
	if cfg.PullPolicy != corev1.PullIfNotPresent && cfg.PullPolicy != corev1.PullAlways && cfg.PullPolicy != corev1.PullNever {
		return nil, fmt.Errorf("invalid image pull policy")
	}
	root := path.Join("/data/workflows", id)
	input, output := path.Join(root, "request.json"), path.Join(root, "scene.svg")
	if stage == "render" {
		input, output = output, path.Join(root, "final.svg")
	}
	var retries int32
	ttl := int32(86400)
	user := int64(10001)
	no := false
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "kubereel-" + id + "-" + stage, Labels: map[string]string{"app.kubernetes.io/name": "kubereel", "kubereel.io/workflow": id, "kubereel.io/stage": stage, "kubereel.io/instance": cfg.Instance}},
		Spec: batchv1.JobSpec{BackoffLimit: &retries, TTLSecondsAfterFinished: &ttl, Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "kubereel", "kubereel.io/workflow": id, "kubereel.io/instance": cfg.Instance}},
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no,
				SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: boolPtr(true), RunAsUser: &user, RunAsGroup: &user, FSGroup: &user},
				Containers: []corev1.Container{{Name: stage, Image: cfg.Image, ImagePullPolicy: cfg.PullPolicy, Args: []string{stage, "--input", input, "--output", output},
					Resources:    corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}},
				Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cfg.Claim}}}},
			},
		}},
	}
	if !cfg.SharedStorage {
		job.Spec.Template.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubereel.io/control-plane": cfg.Instance}}, TopologyKey: "kubernetes.io/hostname"}}}}
	}
	return job, nil
}

func boolPtr(v bool) *bool { return &v }
