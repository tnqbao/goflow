package config

import (
	"fmt"
	"os"

	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	StorageTypePVC = "pvc"
	StorageTypeS3  = "s3"

	defaultS3AccessKeyIDKey     = "accessKeyId"
	defaultS3SecretAccessKeyKey = "secretAccessKey"
)

// S3StorageConfig configures S3-compatible object storage for artifacts.
type S3StorageConfig struct {
	Bucket             string
	Region             string
	Prefix             string
	Endpoint           string
	ForcePathStyle     bool
	CredentialsSecret  string
	AccessKeyIDKey     string
	SecretAccessKeyKey string
}

// StorageConfig selects between PVC filesystem storage and S3-compatible object storage.
type StorageConfig struct {
	Type string
	S3   S3StorageConfig
}

func loadStorageConfig() (StorageConfig, error) {
	storageType := os.Getenv(EnvStorageType)
	if storageType == "" || storageType == StorageTypePVC {
		return StorageConfig{Type: StorageTypePVC}, nil
	}
	if storageType != StorageTypeS3 {
		return StorageConfig{}, fmt.Errorf("unsupported storage type %q", storageType)
	}

	s3Cfg, err := loadS3StorageConfig()
	if err != nil {
		return StorageConfig{}, err
	}

	return StorageConfig{
		Type: StorageTypeS3,
		S3:   s3Cfg,
	}, nil
}

func loadS3StorageConfig() (S3StorageConfig, error) {
	bucket := os.Getenv(EnvS3Bucket)
	if err := storage.ValidateS3Bucket(bucket); err != nil {
		return S3StorageConfig{}, err
	}

	region := os.Getenv(EnvS3Region)
	if err := storage.ValidateS3Region(region); err != nil {
		return S3StorageConfig{}, err
	}

	prefix, err := storage.NormalizeS3Prefix(os.Getenv(EnvS3Prefix))
	if err != nil {
		return S3StorageConfig{}, err
	}

	endpoint := os.Getenv(EnvS3Endpoint)
	if err := storage.ValidateS3Endpoint(endpoint); err != nil {
		return S3StorageConfig{}, err
	}

	forcePathStyle, err := parseBoolEnv(EnvS3ForcePathStyle, false)
	if err != nil {
		return S3StorageConfig{}, err
	}

	credentialsSecret := os.Getenv(EnvS3CredentialsSecret)
	accessKeyIDKey := ""
	secretAccessKeyKey := ""
	if credentialsSecret != "" {
		accessKeyIDKey = envOrDefault(EnvS3AccessKeyIDKey, defaultS3AccessKeyIDKey)
		secretAccessKeyKey = envOrDefault(EnvS3SecretAccessKeyKey, defaultS3SecretAccessKeyKey)
	}

	return S3StorageConfig{
		Bucket:             bucket,
		Region:             region,
		Prefix:             prefix,
		Endpoint:           endpoint,
		ForcePathStyle:     forcePathStyle,
		CredentialsSecret:  credentialsSecret,
		AccessKeyIDKey:     accessKeyIDKey,
		SecretAccessKeyKey: secretAccessKeyKey,
	}, nil
}

func envOrDefault(envName, fallback string) string {
	val := os.Getenv(envName)
	if val == "" {
		return fallback
	}
	return val
}
