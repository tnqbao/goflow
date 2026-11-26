package config

import (
	"fmt"
	"os"
	"strconv"
)

const (
	executionModeKubernetes  = "kubernetes"
	defaultMaxConcurrentJobs = 4
)

// KubernetesConfig holds settings for running the control plane inside Kubernetes.
type KubernetesConfig struct {
	Enabled              bool
	Namespace            string
	JobImage             string
	ImagePullPolicy      string
	PVCName              string
	InstanceName         string
	SharedStorage        bool
	WorkerServiceAccount string
	MaxConcurrentJobs    int
	JobTTLSeconds        *int32
}

func loadKubernetesConfig() (KubernetesConfig, error) {
	mode := os.Getenv(EnvExecutionMode)
	if mode == "" {
		return KubernetesConfig{}, nil
	}

	if mode != executionModeKubernetes {
		return KubernetesConfig{}, fmt.Errorf("unsupported execution mode %q", mode)
	}

	namespace := os.Getenv(EnvNamespace)
	if namespace == "" {
		return KubernetesConfig{}, fmt.Errorf("%s is required", EnvNamespace)
	}

	sharedStorage, err := parseBoolEnv(EnvSharedStorage, false)
	if err != nil {
		return KubernetesConfig{}, err
	}

	maxConcurrentJobs, err := parsePositiveIntEnv(EnvMaxConcurrentJobs, defaultMaxConcurrentJobs)
	if err != nil {
		return KubernetesConfig{}, err
	}

	jobTTLSeconds, err := ParseJobTTL(os.Getenv(EnvJobTTLSeconds))
	if err != nil {
		return KubernetesConfig{}, err
	}

	return KubernetesConfig{
		Enabled:              true,
		Namespace:            namespace,
		JobImage:             os.Getenv(EnvJobImage),
		ImagePullPolicy:      os.Getenv(EnvImagePullPolicy),
		PVCName:              os.Getenv(EnvPVCName),
		InstanceName:         os.Getenv(EnvInstanceName),
		SharedStorage:        sharedStorage,
		WorkerServiceAccount: os.Getenv(EnvWorkerServiceAccount),
		MaxConcurrentJobs:    maxConcurrentJobs,
		JobTTLSeconds:        jobTTLSeconds,
	}, nil
}

// ParseJobTTL parses an optional non-negative 32-bit integer TTL in seconds.
func ParseJobTTL(rawValue string) (*int32, error) {
	if rawValue == "" {
		return nil, nil
	}

	parsed, err := strconv.ParseInt(rawValue, 10, 32)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", EnvJobTTLSeconds)
	}

	ttl := int32(parsed)
	return &ttl, nil
}

func parseBoolEnv(envName string, fallback bool) (bool, error) {
	rawValue := os.Getenv(envName)
	if rawValue == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseBool(rawValue)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", envName, err)
	}

	return parsed, nil
}

func parsePositiveIntEnv(envName string, fallback int) (int, error) {
	rawValue := os.Getenv(envName)
	if rawValue == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(rawValue)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", envName)
	}

	return parsed, nil
}
