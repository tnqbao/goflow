package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/tnqbao/kubereel/internal/workflow"
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

// VideoRunResource identifies the VideoRun API resource.
func VideoRunResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "kubereel.io", Version: "v1alpha1", Resource: "videoruns"}
}

// VideoRunController validates workflow snapshots and records ready steps.
type VideoRunController struct {
	Runs dynamic.ResourceInterface
}

// Run watches VideoRuns until the context is cancelled.
func (c *VideoRunController) Run(ctx context.Context) {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "videoruns"})
	defer queue.ShutDown()
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{
		ListFunc:  func(options metav1.ListOptions) (runtime.Object, error) { return c.Runs.List(ctx, options) },
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) { return c.Runs.Watch(ctx, options) },
	}, &unstructured.Unstructured{}, 0, nil)
	enqueue := func(object interface{}) {
		key, err := cache.MetaNamespaceKeyFunc(object)
		if err != nil {
			slog.Error("queue VideoRun", "error", err)
			return
		}
		queue.Add(key)
	}
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		UpdateFunc: func(_, current interface{}) { enqueue(current) },
	})
	if err != nil {
		slog.Error("register VideoRun event handler", "error", err)
		return
	}
	go informer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return
	}
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for c.process(ctx, queue) {
	}
}

func (c *VideoRunController) process(ctx context.Context, queue workqueue.TypedRateLimitingInterface[string]) bool {
	item, shutdown := queue.Get()
	if shutdown {
		return false
	}
	defer queue.Done(item)
	_, name, err := cache.SplitMetaNamespaceKey(item)
	if err == nil {
		err = c.Reconcile(ctx, name)
	}
	if err == nil || errors.Is(err, context.Canceled) {
		queue.Forget(item)
		return true
	}
	slog.Error("reconcile VideoRun", "key", item, "error", err)
	queue.AddRateLimited(item)
	return true
}

// Reconcile updates a single VideoRun using the current resource version.
func (c *VideoRunController) Reconcile(ctx context.Context, name string) error {
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
	cancelled, _, err := unstructured.NestedBool(run.Object, "spec", "cancelRequested")
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	phase, _, err := unstructured.NestedString(run.Object, "status", "phase")
	if err != nil {
		return err
	}
	if phase != "" && phase != "Pending" && phase != "Running" {
		return nil
	}
	status, err := videoRunStatus(run)
	if err != nil {
		return err
	}
	current, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return err
	}
	if reflect.DeepEqual(current, status) {
		return nil
	}
	updated := run.DeepCopy()
	if err := unstructured.SetNestedMap(updated.Object, status, "status"); err != nil {
		return err
	}
	if _, err := c.Runs.UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update VideoRun status: %w", err)
	}
	return nil
}

func videoRunStatus(run *unstructured.Unstructured) (map[string]interface{}, error) {
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
	status, _, err := unstructured.NestedMap(run.Object, "status")
	if err != nil {
		return nil, err
	}
	if status == nil {
		status = make(map[string]interface{})
	}
	if err := workflow.ValidateDAG(graph.Steps); err != nil {
		status["phase"] = "Failed"
		status["error"] = err.Error()
		return status, nil
	}
	if status["phase"] == nil || status["phase"] == "" {
		status["phase"] = "Pending"
	}
	steps, _, err := unstructured.NestedSlice(status, "steps")
	if err != nil {
		return nil, err
	}
	byName := make(map[string]map[string]interface{}, len(steps))
	for _, item := range steps {
		step, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid step status")
		}
		name, ok := step["name"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid step status name")
		}
		byName[name] = step
	}
	for _, step := range graph.Steps {
		if _, exists := byName[step.Name]; !exists {
			entry := map[string]interface{}{"name": step.Name, "phase": "Pending"}
			byName[step.Name] = entry
			steps = append(steps, entry)
		}
	}
	for _, step := range graph.Steps {
		entry := byName[step.Name]
		ready := entry["phase"] == "Pending"
		for _, dependency := range step.DependsOn {
			if byName[dependency]["phase"] != "Succeeded" {
				ready = false
			}
		}
		entry["ready"] = ready
	}
	status["steps"] = steps
	return status, nil
}
