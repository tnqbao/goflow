package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/kubernetes"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func controllerFromEnv(ctx context.Context, store *workflow.Store, artifacts storage.Store, cfg config.Config) (*kubernetes.Controller, error) {
	mode := os.Getenv("KUBEREEL_EXECUTION_MODE")
	if mode == "" {
		return nil, nil
	}
	if mode != "kubernetes" {
		return nil, fmt.Errorf("unsupported execution mode %q", mode)
	}
	namespace := os.Getenv("KUBEREEL_NAMESPACE")
	config := kubernetes.JobConfig{Image: os.Getenv("KUBEREEL_JOB_IMAGE"), Claim: os.Getenv("KUBEREEL_PVC"), Instance: os.Getenv("KUBEREEL_INSTANCE"), PullPolicy: corev1.PullPolicy(os.Getenv("KUBEREEL_IMAGE_PULL_POLICY")), Artifacts: &kubernetes.ArtifactConfig{MaxBytes: cfg.ArtifactMaxBytes, RetentionSeconds: int64(cfg.ArtifactRetention / time.Second)}}
	ttl, err := parseJobTTL(os.Getenv("KUBEREEL_JOB_TTL_SECONDS"))
	if err != nil {
		return nil, err
	}
	config.TTLSecondsAfterFinished = ttl
	if namespace == "" {
		return nil, fmt.Errorf("KUBEREEL_NAMESPACE is required")
	}
	shared := os.Getenv("KUBEREEL_SHARED_STORAGE")
	if shared != "" {
		value, err := strconv.ParseBool(shared)
		if err != nil {
			return nil, fmt.Errorf("invalid KUBEREEL_SHARED_STORAGE: %w", err)
		}
		config.SharedStorage = value
	}
	maximum := 4
	if value := os.Getenv("KUBEREEL_MAX_CONCURRENT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("KUBEREEL_MAX_CONCURRENT must be a positive integer")
		}
		maximum = parsed
	}
	if _, err := kubernetes.Job(strings.Repeat("0", 32), "worker", config); err != nil {
		return nil, fmt.Errorf("invalid Kubernetes job config: %w", err)
	}
	cluster, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("configure Kubernetes client: %w", err)
	}
	batch, err := batchclient.NewForConfig(cluster)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	jobs := batch.Jobs(namespace)
	core, err := coreclient.NewForConfig(cluster)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes storage client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(cluster)
	if err != nil {
		return nil, fmt.Errorf("create VideoRun client: %w", err)
	}
	runs := dynamicClient.Resource(kubernetes.VideoRunResource()).Namespace(namespace)
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := jobs.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("verify Kubernetes job access: %w", err)
	}
	if _, err := runs.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("verify VideoRun access: %w", err)
	}
	claim, err := core.PersistentVolumeClaims(namespace).Get(probeCtx, config.Claim, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("verify Kubernetes storage: %w", err)
	}
	if !supportsStorage(claim.Spec.AccessModes, config.SharedStorage) {
		return nil, fmt.Errorf("PVC access modes do not support configured workflow placement")
	}
	lock := &sync.Mutex{}
	executor := &kubernetes.StepExecutor{Runs: runs, Jobs: jobs, Store: store, Config: config, Artifacts: artifacts, MaxConcurrent: maximum, JobLock: lock}
	return &kubernetes.Controller{Store: store, Jobs: jobs, Config: config, Artifacts: artifacts, MaxConcurrent: maximum, JobLock: lock, VideoRuns: &kubernetes.VideoRunController{Runs: runs, Executor: executor}}, nil
}

func parseJobTTL(value string) (*int32, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("KUBEREEL_JOB_TTL_SECONDS must be a non-negative integer")
	}
	ttl := int32(parsed)
	return &ttl, nil
}

func supportsStorage(modes []corev1.PersistentVolumeAccessMode, shared bool) bool {
	for _, mode := range modes {
		if mode == corev1.ReadWriteMany {
			return true
		}
		if !shared && mode == corev1.ReadWriteOnce {
			return true
		}
	}
	return false
}
