package repository

import (
	"context"
	"errors"
	"fmt"
	"io"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const runLabel = "kubereel.io/videorun"
const maxLogBytes = 65536

var ErrExecutionLogsUnavailable = errors.New("execution logs unavailable")

func (r *ExecutionRepository) HasActiveJobs(ctx context.Context, run *unstructured.Unstructured) (bool, error) {
	options := metav1.ListOptions{LabelSelector: labels.Set{runLabel: string(run.GetUID())}.String(), Limit: 100}
	for {
		page, err := r.jobs.List(ctx, options)
		if err != nil {
			return false, err
		}
		for i := range page.Items {
			job := &page.Items[i]
			if ownedByRun(job, run) && !jobIsFinished(job) {
				return true, nil
			}
		}
		if page.Continue == "" {
			return false, nil
		}
		options.Continue = page.Continue
	}
}

func (r *ExecutionRepository) Logs(ctx context.Context, run *unstructured.Unstructured, step, jobName string, tail int64) (string, error) {
	job, err := r.jobs.Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", ErrExecutionLogsUnavailable
	}
	if err != nil {
		return "", err
	}
	if !ownedByRun(job, run) || job.Annotations["kubereel.io/step"] != step {
		return "", ErrExecutionLogsUnavailable
	}
	pod, err := r.latestJobPod(ctx, job)
	if err != nil {
		return "", err
	}
	stage := job.Labels["kubereel.io/stage"]
	if stage != "worker" && stage != "render" {
		return "", ErrExecutionLogsUnavailable
	}
	limit := int64(maxLogBytes)
	reader, err := r.pods.GetLogs(pod.Name, &corev1.PodLogOptions{Container: stage, TailLines: &tail, LimitBytes: &limit}).Stream(ctx)
	if apierrors.IsNotFound(err) || apierrors.IsBadRequest(err) {
		return "", ErrExecutionLogsUnavailable
	}
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, limit))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return "", fmt.Errorf("read execution logs: %w", err)
	}
	return string(data), nil
}

func (r *ExecutionRepository) latestJobPod(ctx context.Context, job *batchv1.Job) (*corev1.Pod, error) {
	options := metav1.ListOptions{LabelSelector: labels.Set{"batch.kubernetes.io/controller-uid": string(job.UID)}.String(), Limit: 100}
	var latest *corev1.Pod
	for {
		page, err := r.pods.List(ctx, options)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			pod := &page.Items[i]
			if !metav1.IsControlledBy(pod, job) {
				continue
			}
			if latest == nil || latest.CreationTimestamp.Time.Before(pod.CreationTimestamp.Time) {
				latest = pod
			}
		}
		if page.Continue == "" {
			break
		}
		options.Continue = page.Continue
	}
	if latest == nil {
		return nil, ErrExecutionLogsUnavailable
	}
	return latest, nil
}

func ownedByRun(job *batchv1.Job, run *unstructured.Unstructured) bool {
	owner := metav1.GetControllerOf(job)
	return owner != nil && owner.UID == run.GetUID() && owner.Name == run.GetName() && owner.Kind == "VideoRun" && owner.APIVersion == VideoRunResource().GroupVersion().String()
}

func jobIsFinished(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) {
			return true
		}
	}
	return false
}

// ProviderSummary reads the bounded termination message of a verified step worker.
func (r *ExecutionRepository) ProviderSummary(ctx context.Context, run *unstructured.Unstructured, step, jobName string) (string, error) {
	job, err := r.jobs.Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !ownedByRun(job, run) || job.Annotations["kubereel.io/step"] != step {
		return "", nil
	}
	pod, err := r.latestJobPod(ctx, job)
	if errors.Is(err, ErrExecutionLogsUnavailable) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "worker" && status.State.Terminated != nil {
			return status.State.Terminated.Message, nil
		}
	}
	return "", nil
}
