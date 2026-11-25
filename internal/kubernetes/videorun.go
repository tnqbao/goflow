package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const informerResyncPeriod = time.Minute

// VideoRunResource identifies the VideoRun Custom Resource in the kubereel.io group.
func VideoRunResource() schema.GroupVersionResource {
	return repository.VideoRunResource()
}

// VideoProviderResource identifies the VideoProvider Custom Resource in the kubereel.io group.
func VideoProviderResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kubereel.io",
		Version:  "v1alpha1",
		Resource: "videoproviders",
	}
}

// VideoRunController validates workflow snapshots and reconciles VideoRun executions.
type VideoRunController struct {
	Observer runObserver
	Runs     dynamic.ResourceInterface
	Executor *StepExecutor
}

// Run watches VideoRuns and owned Jobs until ctx is cancelled.
func (c *VideoRunController) Run(ctx context.Context) {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "videoruns"},
	)
	defer queue.ShutDown()

	runInformer, err := c.newVideoRunInformer(ctx, queue)
	if err != nil {
		slog.Error("register VideoRun event handler", "error", err)
		return
	}

	var jobInformer cache.SharedIndexInformer
	if c.Executor != nil {
		jobInformer, err = c.newJobInformer(ctx, runInformer, queue)
		if err != nil {
			slog.Error("register VideoRun job event handler", "error", err)
			return
		}
	}

	if jobInformer != nil {
		go jobInformer.Run(ctx.Done())
	}
	go runInformer.Run(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), runInformer.HasSynced) {
		return
	}
	if jobInformer != nil && !cache.WaitForCacheSync(ctx.Done(), jobInformer.HasSynced) {
		return
	}

	c.Observer.installInventory(runInformer, jobInformer)

	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()

	for c.processNextWorkItem(ctx, queue) {
	}
}

func (c *VideoRunController) newVideoRunInformer(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[string],
) (cache.SharedIndexInformer, error) {
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return c.Runs.List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return c.Runs.Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		informerResyncPeriod,
		nil,
	)

	enqueueRun := func(obj interface{}) {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			slog.Error("queue VideoRun", "error", err)
			return
		}
		queue.Add(key)
	}

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueueRun,
		UpdateFunc: func(_, current interface{}) { enqueueRun(current) },
	})

	return informer, err
}

func (c *VideoRunController) newJobInformer(
	ctx context.Context,
	runInformer cache.SharedIndexInformer,
	queue workqueue.TypedRateLimitingInterface[string],
) (cache.SharedIndexInformer, error) {
	selector := "kubereel.io/instance=" + c.Executor.Config.Instance

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = selector
				return c.Executor.Jobs.List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = selector
				return c.Executor.Jobs.Watch(ctx, options)
			},
		},
		&batchv1.Job{},
		0,
		nil,
	)

	enqueueAllRuns := func(_ interface{}) {
		for _, key := range runInformer.GetStore().ListKeys() {
			queue.Add(key)
		}
	}

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueueAllRuns,
		UpdateFunc: func(_, current interface{}) { enqueueAllRuns(current) },
		DeleteFunc: enqueueAllRuns,
	})

	return informer, err
}

func (c *VideoRunController) processNextWorkItem(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[string],
) bool {
	itemKey, shutdown := queue.Get()
	if shutdown {
		return false
	}
	defer queue.Done(itemKey)

	_, runName, err := cache.SplitMetaNamespaceKey(itemKey)
	if err == nil {
		err = c.Reconcile(ctx, runName)
	}

	if err == nil || errors.Is(err, context.Canceled) {
		queue.Forget(itemKey)
		return true
	}

	slog.Error("reconcile VideoRun", "key", itemKey, "error", err)
	queue.AddRateLimited(itemKey)
	return true
}

// Reconcile updates a single VideoRun using its current resource version.
func (c *VideoRunController) Reconcile(ctx context.Context, name string) error {
	ctx = context.WithValue(ctx, runObserverKey{}, c.Observer)
	run, err := c.Runs.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get VideoRun: %w", err)
	}

	if run.GetDeletionTimestamp() != nil {
		return nil
	}

	phase, _, err := unstructured.NestedString(run.Object, "status", "phase")
	if err != nil {
		return err
	}

	if phase == "Succeeded" && c.Executor != nil {
		return c.Executor.cleanupArtifacts(ctx, run, time.Now())
	}

	cancelRequested, _, err := unstructured.NestedBool(run.Object, "spec", "cancelRequested")
	if err != nil {
		return err
	}

	if cancelRequested {
		if c.Executor == nil {
			return nil
		}
		return c.Executor.Cancel(ctx, run)
	}

	if c.Executor != nil && (phase == "Failed" || phase == "RequiresReview" || phase == "Cancelling") {
		return c.Executor.Cancel(ctx, run)
	}

	if phase != "" && phase != "Pending" && phase != "Running" {
		return nil
	}

	desiredStatus, err := computeVideoRunStatus(run)
	if err != nil {
		return err
	}

	currentStatus, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(currentStatus, desiredStatus) {
		return updateVideoRunStatus(ctx, c.Runs, run, desiredStatus)
	}

	if c.Executor == nil {
		return nil
	}

	return c.Executor.Reconcile(ctx, run, desiredStatus)
}

func updateVideoRunStatus(
	ctx context.Context,
	runs dynamic.ResourceInterface,
	run *unstructured.Unstructured,
	status map[string]interface{},
) error {
	previous, _, _ := unstructured.NestedMap(run.Object, "status")
	stampRunStatus(previous, status, time.Now())
	updated := run.DeepCopy()
	if err := unstructured.SetNestedMap(updated.Object, status, "status"); err != nil {
		return err
	}

	if _, err := runs.UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update VideoRun status: %w", err)
	}

	observer, _ := ctx.Value(runObserverKey{}).(runObserver)
	observer.record(ctx, run, previous, status)
	return nil
}

func computeVideoRunStatus(run *unstructured.Unstructured) (map[string]interface{}, error) {
	dagSteps, err := decodeSnapshotDAGSteps(run)
	if err != nil {
		return nil, err
	}

	status, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return nil, err
	}
	if status == nil {
		status = make(map[string]interface{})
	}

	if err := workflow.ValidateDAG(dagSteps); err != nil {
		status["phase"] = "Failed"
		status["error"] = err.Error()
		return status, nil
	}

	if status["phase"] == nil || status["phase"] == "" {
		status["phase"] = "Pending"
	}

	stepEntries, byName, err := stepStatuses(status)
	if err != nil {
		return nil, err
	}

	stepEntries = ensureStepEntries(dagSteps, stepEntries, byName)
	refreshStepReadiness(dagSteps, byName)

	status["steps"] = stepEntries
	return status, nil
}

func decodeSnapshotDAGSteps(run *unstructured.Unstructured) ([]workflow.DAGStep, error) {
	snapshot, found, err := unstructured.NestedMap(run.Object, "spec", "workflowSnapshot")
	if err != nil {
		return nil, fmt.Errorf("read workflow snapshot: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("workflow snapshot is missing")
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode workflow snapshot: %w", err)
	}

	var graph struct {
		Steps []workflow.DAGStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("decode workflow snapshot: %w", err)
	}

	return graph.Steps, nil
}

func ensureStepEntries(
	dagSteps []workflow.DAGStep,
	stepEntries []interface{},
	byName map[string]map[string]interface{},
) []interface{} {
	for _, step := range dagSteps {
		if _, exists := byName[step.Name]; exists {
			continue
		}

		entry := map[string]interface{}{
			"name":  step.Name,
			"phase": "Pending",
		}
		byName[step.Name] = entry
		stepEntries = append(stepEntries, entry)
	}

	return stepEntries
}

func refreshStepReadiness(dagSteps []workflow.DAGStep, byName map[string]map[string]interface{}) {
	for _, step := range dagSteps {
		entry := byName[step.Name]
		ready := entry["phase"] == "Pending"

		for _, dependency := range step.DependsOn {
			if byName[dependency]["phase"] != "Succeeded" {
				ready = false
				break
			}
		}

		entry["ready"] = ready
	}
}
