package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/kubernetes"
	"github.com/tnqbao/kubereel/internal/workflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	"k8s.io/client-go/rest"
)

func controllerFromEnv(ctx context.Context, store *workflow.Store, dataDir string) (*kubernetes.Controller, error) {
	mode := os.Getenv("KUBEREEL_EXECUTION_MODE")
	if mode == "" {
		return nil, nil
	}
	if mode != "kubernetes" {
		return nil, fmt.Errorf("unsupported execution mode %q", mode)
	}
	namespace := os.Getenv("KUBEREEL_NAMESPACE")
	config := kubernetes.JobConfig{Image: os.Getenv("KUBEREEL_JOB_IMAGE"), Claim: os.Getenv("KUBEREEL_PVC"), Instance: os.Getenv("KUBEREEL_INSTANCE")}
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
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := jobs.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("verify Kubernetes job access: %w", err)
	}
	return &kubernetes.Controller{Store: store, Jobs: jobs, Config: config, DataDir: dataDir, MaxConcurrent: maximum}, nil
}
