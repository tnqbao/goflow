package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func activeVideoJobs(ctx context.Context, jobs Jobs, instance string) (int, error) {
	list, err := jobs.List(ctx, metav1.ListOptions{LabelSelector: "kubereel.io/instance=" + instance + ",kubereel.io/videorun"})
	if err != nil {
		return 0, fmt.Errorf("list VideoRun jobs: %w", err)
	}
	count := 0
	for i := range list.Items {
		job := &list.Items[i]
		if finished(job, batchv1.JobComplete) || finished(job, batchv1.JobFailed) {
			continue
		}
		count++
	}
	return count, nil
}

// StepJob builds a deterministic Mock image Job owned by a VideoRun.
func StepJob(runName string, uid types.UID, stepName string, cfg JobConfig) (*batchv1.Job, error) {
	if runName == "" || uid == "" || stepName == "" {
		return nil, fmt.Errorf("run name, UID and step name are required")
	}
	hash := sha256.Sum256([]byte(string(uid) + "\x00" + stepName))
	id := hex.EncodeToString(hash[:16])
	job, err := Job(id, "worker", cfg)
	if err != nil {
		return nil, err
	}
	job.Labels["kubereel.io/videorun"] = string(uid)
	job.Annotations = map[string]string{"kubereel.io/step": stepName}
	job.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubereel.io/v1alpha1", Kind: "VideoRun", Name: runName, UID: uid, Controller: boolPtr(true)}}
	return job, nil
}

func validateStepJob(actual, expected *batchv1.Job) error {
	if err := validateJob(actual, expected); err != nil {
		return err
	}
	if !apiequality.Semantic.DeepEqual(actual.OwnerReferences, expected.OwnerReferences) || actual.Annotations["kubereel.io/step"] != expected.Annotations["kubereel.io/step"] {
		return fmt.Errorf("step Job ownership mismatch")
	}
	if !apiequality.Semantic.DeepEqual(actual.Spec.ActiveDeadlineSeconds, expected.Spec.ActiveDeadlineSeconds) || !apiequality.Semantic.DeepEqual(actual.Spec.BackoffLimit, expected.Spec.BackoffLimit) || !apiequality.Semantic.DeepEqual(actual.Spec.TTLSecondsAfterFinished, expected.Spec.TTLSecondsAfterFinished) {
		return fmt.Errorf("step Job execution configuration mismatch")
	}
	pod, wanted := actual.Spec.Template.Spec, expected.Spec.Template.Spec
	if len(pod.InitContainers) != 0 || len(pod.EphemeralContainers) != 0 || pod.HostNetwork || pod.HostPID || pod.HostIPC {
		return fmt.Errorf("step Job pod configuration mismatch")
	}
	if !apiequality.Semantic.DeepEqual(pod.Volumes, wanted.Volumes) || !apiequality.Semantic.DeepEqual(pod.SecurityContext, wanted.SecurityContext) || !apiequality.Semantic.DeepEqual(pod.AutomountServiceAccountToken, wanted.AutomountServiceAccountToken) || !apiequality.Semantic.DeepEqual(pod.Affinity, wanted.Affinity) {
		return fmt.Errorf("step Job pod configuration mismatch")
	}
	container, expectedContainer := pod.Containers[0], wanted.Containers[0]
	if len(container.Command) != 0 || len(container.Env) != 0 || len(container.EnvFrom) != 0 || container.WorkingDir != "" || container.ImagePullPolicy != expectedContainer.ImagePullPolicy || !apiequality.Semantic.DeepEqual(container.SecurityContext, expectedContainer.SecurityContext) {
		return fmt.Errorf("step Job container configuration mismatch")
	}
	if container.Lifecycle != nil || container.LivenessProbe != nil || container.ReadinessProbe != nil || container.StartupProbe != nil || len(container.Ports) != 0 || container.Stdin || container.TTY {
		return fmt.Errorf("step Job container configuration mismatch")
	}
	if !apiequality.Semantic.DeepEqual(container.VolumeMounts, expectedContainer.VolumeMounts) || !apiequality.Semantic.DeepEqual(container.Resources, expectedContainer.Resources) {
		return fmt.Errorf("step Job container configuration mismatch")
	}
	return nil
}

func configureStepJob(job *batchv1.Job, step executableStep) error {
	if step.TimeoutSeconds < 0 || step.TimeoutSeconds > 86400 {
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
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, limit := resources.Requests[name], resources.Limits[name]
		if request.Cmp(limit) > 0 {
			return fmt.Errorf("resource request exceeds limit")
		}
	}
	return nil
}

func applyResourceValues(target corev1.ResourceList, values map[string]string) error {
	for name, value := range values {
		if name != "cpu" && name != "memory" {
			return fmt.Errorf("unsupported resource")
		}
		quantity, err := resource.ParseQuantity(value)
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
