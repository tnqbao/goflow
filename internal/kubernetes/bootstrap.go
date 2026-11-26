package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/observability"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

const clusterProbeTimeout = 10 * time.Second

type clusterClients struct {
	workflows dynamic.ResourceInterface
	jobs      batchclient.JobInterface
	core      coreclient.CoreV1Interface
	runs      dynamic.ResourceInterface
	providers dynamic.ResourceInterface
}

// NewControllerFromCluster connects to the in-cluster Kubernetes API, verifies RBAC
// and PVC access modes, and returns a configured Controller when Kubernetes mode is enabled.
func NewControllerFromCluster(
	ctx context.Context,
	cfg config.Config,
	workflows *service.WorkflowService,
	artifacts storage.Store,
	metrics *observability.Metrics,
) (*Controller, error) {
	if !cfg.Kubernetes.Enabled {
		return nil, nil
	}

	jobCfg, err := buildJobConfig(cfg)
	if err != nil {
		return nil, err
	}

	clients, err := newClusterClients(cfg.Kubernetes.Namespace)
	if err != nil {
		return nil, err
	}

	if err := verifyClusterAccess(ctx, clients, cfg.Kubernetes.Namespace, jobCfg); err != nil {
		return nil, err
	}

	jobLock := &sync.Mutex{}
	executor := &StepExecutor{
		Runs:          clients.runs,
		Providers:     clients.providers,
		Jobs:          clients.jobs,
		Workflows:     workflows,
		Config:        jobCfg,
		Artifacts:     artifacts,
		MaxConcurrent: cfg.Kubernetes.MaxConcurrentJobs,
		JobLock:       jobLock,
	}

	definitions := repository.NewWorkflowDefinitionRepository(clients.workflows)
	runs := repository.NewExecutionRepository(clients.runs, clients.jobs, clients.core.Pods(cfg.Kubernetes.Namespace))
	observer := runObserver{repository: runs, metrics: metrics}
	return &Controller{
		Metrics:             metrics,
		Schedules:           service.NewSchedules(definitions, runs),
		ExecutionRepository: runs,
		WorkflowDefinitions: definitions,
		ProviderRepository:  repository.NewProviderRepository(clients.providers, clients.core.Secrets(cfg.Kubernetes.Namespace)),
		Workflows:           workflows,
		Jobs:                clients.jobs,
		Config:              jobCfg,
		Artifacts:           artifacts,
		MaxConcurrent:       cfg.Kubernetes.MaxConcurrentJobs,
		JobLock:             jobLock,
		VideoRuns: &VideoRunController{
			Observer: observer,
			Runs:     clients.runs,
			Executor: executor,
		},
	}, nil
}

func buildJobConfig(cfg config.Config) (JobConfig, error) {
	k8sCfg := cfg.Kubernetes
	jobCfg := JobConfig{
		Image:                   k8sCfg.JobImage,
		Claim:                   k8sCfg.PVCName,
		Instance:                k8sCfg.InstanceName,
		SharedStorage:           k8sCfg.SharedStorage,
		S3:                      buildS3JobConfig(cfg.Storage),
		WorkerServiceAccount:    k8sCfg.WorkerServiceAccount,
		PullPolicy:              corev1.PullPolicy(k8sCfg.ImagePullPolicy),
		TTLSecondsAfterFinished: k8sCfg.JobTTLSeconds,
		Artifacts: &ArtifactConfig{
			MaxBytes:         cfg.ArtifactMaxBytes,
			RetentionSeconds: int64(cfg.ArtifactRetention / time.Second),
		},
	}

	sampleID := strings.Repeat("0", 32)
	if _, err := Job(sampleID, StageWorker, jobCfg); err != nil {
		return JobConfig{}, fmt.Errorf("invalid Kubernetes job config: %w", err)
	}

	return jobCfg, nil
}

func buildS3JobConfig(storageCfg config.StorageConfig) *S3JobConfig {
	if storageCfg.Type != config.StorageTypeS3 {
		return nil
	}

	s3Cfg := &S3JobConfig{
		Bucket:         storageCfg.S3.Bucket,
		Region:         storageCfg.S3.Region,
		Prefix:         storageCfg.S3.Prefix,
		Endpoint:       storageCfg.S3.Endpoint,
		ForcePathStyle: storageCfg.S3.ForcePathStyle,
	}
	if storageCfg.S3.CredentialsSecret != "" {
		s3Cfg.Credentials = &S3SecretRefs{
			Name:               storageCfg.S3.CredentialsSecret,
			AccessKeyIDKey:     storageCfg.S3.AccessKeyIDKey,
			SecretAccessKeyKey: storageCfg.S3.SecretAccessKeyKey,
		}
	}

	return s3Cfg
}

func newClusterClients(namespace string) (clusterClients, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return clusterClients{}, fmt.Errorf("configure Kubernetes client: %w", err)
	}

	batchClient, err := batchclient.NewForConfig(restCfg)
	if err != nil {
		return clusterClients{}, fmt.Errorf("create Kubernetes batch client: %w", err)
	}

	coreClient, err := coreclient.NewForConfig(restCfg)
	if err != nil {
		return clusterClients{}, fmt.Errorf("create Kubernetes core client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return clusterClients{}, fmt.Errorf("create VideoRun client: %w", err)
	}

	return clusterClients{
		workflows: dynamicClient.Resource(repository.VideoWorkflowResource()).Namespace(namespace),
		jobs:      batchClient.Jobs(namespace),
		core:      coreClient,
		runs:      dynamicClient.Resource(VideoRunResource()).Namespace(namespace),
		providers: dynamicClient.Resource(VideoProviderResource()).Namespace(namespace),
	}, nil
}

func verifyClusterAccess(ctx context.Context, clients clusterClients, namespace string, jobCfg JobConfig) error {
	probeCtx, cancel := context.WithTimeout(ctx, clusterProbeTimeout)
	defer cancel()

	if _, err := clients.jobs.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("verify Kubernetes job access: %w", err)
	}

	if _, err := clients.runs.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("verify VideoRun access: %w", err)
	}

	if _, err := clients.providers.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("verify VideoProvider access: %w", err)
	}

	if _, err := clients.workflows.List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("verify VideoWorkflow access: %w", err)
	}

	if jobCfg.S3 != nil {
		return verifyS3StorageAccess(probeCtx, clients, namespace, jobCfg.S3)
	}

	claim, err := clients.core.PersistentVolumeClaims(namespace).Get(probeCtx, jobCfg.Claim, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verify Kubernetes storage: %w", err)
	}

	if !SupportsStorageAccessModes(claim.Spec.AccessModes, jobCfg.SharedStorage) {
		return fmt.Errorf("PVC access modes do not support configured workflow placement")
	}

	return nil
}

func verifyS3StorageAccess(ctx context.Context, clients clusterClients, namespace string, s3Cfg *S3JobConfig) error {
	if s3Cfg.Credentials == nil {
		return nil
	}

	secret, err := clients.core.Secrets(namespace).Get(ctx, s3Cfg.Credentials.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verify S3 credentials secret %q: %w", s3Cfg.Credentials.Name, err)
	}
	if len(secret.Data[s3Cfg.Credentials.AccessKeyIDKey]) == 0 || len(secret.Data[s3Cfg.Credentials.SecretAccessKeyKey]) == 0 {
		return fmt.Errorf("S3 credentials secret %q is missing required keys", s3Cfg.Credentials.Name)
	}

	return nil
}

// SupportsStorageAccessModes reports whether PVC access modes are compatible with shared multi-node or single-node RWO placement.
func SupportsStorageAccessModes(modes []corev1.PersistentVolumeAccessMode, shared bool) bool {
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
