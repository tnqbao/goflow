package repository

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func TestProviderSummaryRequiresOwnedJobAndPod(t *testing.T) {
	ctx := context.Background()
	run := &unstructured.Unstructured{}
	run.SetName("run")
	run.SetUID(types.UID("run-uid"))
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "job", Namespace: "test", UID: "job-uid",
		Annotations: map[string]string{"kubereel.io/step": "scene"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: VideoRunResource().GroupVersion().String(), Kind: "VideoRun", Name: "run", UID: "run-uid", Controller: &controller,
		}},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "pod", Namespace: "test", Labels: map[string]string{"batch.kubernetes.io/controller-uid": "job-uid"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: "Job", Name: "job", UID: "job-uid", Controller: &controller,
		}},
	}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "worker", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: `{"fal":{"count":1}}`}},
	}}}}
	client := corefake.NewSimpleClientset(job, pod)
	repo := NewExecutionRepository(nil, client.BatchV1().Jobs("test"), client.CoreV1().Pods("test"))
	raw, err := repo.ProviderSummary(ctx, run, "scene", "job")
	if err != nil || raw != pod.Status.ContainerStatuses[0].State.Terminated.Message {
		t.Fatalf("summary = %q, %v", raw, err)
	}
	run.SetUID(types.UID("other-run"))
	raw, err = repo.ProviderSummary(ctx, run, "scene", "job")
	if err != nil || raw != "" {
		t.Fatalf("foreign summary = %q, %v", raw, err)
	}
}
