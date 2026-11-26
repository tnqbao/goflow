package kubernetes

import (
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/config"
)

func TestJobRespectsStorageAndStages(t *testing.T) {
	id := strings.Repeat("a", 32)
	cfg := JobConfig{
		Image:    "example/kubereel:v1",
		Claim:    "data",
		Instance: "test",
	}

	for _, stage := range []string{StageWorker, StageRender} {
		job, err := Job(id, stage, cfg)
		if err != nil {
			t.Fatal(err)
		}

		pod := job.Spec.Template.Spec
		if pod.Affinity == nil || pod.Affinity.PodAffinity == nil {
			t.Fatal("RWO job has no control-plane affinity")
		}
		if pod.Containers[0].Image != cfg.Image || pod.Containers[0].Args[0] != stage {
			t.Fatal("wrong image or command")
		}
		if pod.Containers[0].ImagePullPolicy != "IfNotPresent" {
			t.Fatal("unexpected image pull policy")
		}
		if pod.Volumes[0].PersistentVolumeClaim.ClaimName != cfg.Claim {
			t.Fatal("wrong PVC")
		}
		if *pod.AutomountServiceAccountToken {
			t.Fatal("worker received API token")
		}
		if *pod.Containers[0].SecurityContext.AllowPrivilegeEscalation || pod.Containers[0].SecurityContext.Capabilities.Drop[0] != "ALL" {
			t.Fatal("worker privileges are not restricted")
		}
		if *job.Spec.BackoffLimit != 0 {
			t.Fatal("unexpected job retry")
		}
	}

	cfg.SharedStorage = true
	job, err := Job(id, StageWorker, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Template.Spec.Affinity != nil {
		t.Fatal("shared storage unexpectedly pins to a node")
	}

	ttl := int32(30)
	cfg.TTLSecondsAfterFinished = &ttl
	job, err = Job(id, StageWorker, cfg)
	if err != nil || *job.Spec.TTLSecondsAfterFinished != ttl {
		t.Fatalf("Job retention: %v, %v", job, err)
	}
}

func TestJobRejectsInvalidInputs(t *testing.T) {
	validID := strings.Repeat("a", 32)
	tests := []struct {
		id    string
		stage string
		cfg   JobConfig
	}{
		{"../id", StageWorker, JobConfig{Image: "i", Claim: "c", Instance: "r"}},
		{validID, "unknown", JobConfig{Image: "i", Claim: "c", Instance: "r"}},
		{validID, StageWorker, JobConfig{}},
	}

	for _, tt := range tests {
		if _, err := Job(tt.id, tt.stage, tt.cfg); err == nil {
			t.Fatalf("accepted %+v", tt)
		}
	}
}

func TestJobRejectsNegativeRetention(t *testing.T) {
	ttl := int32(-1)
	cfg := JobConfig{
		Image:                   "i",
		Claim:                   "c",
		Instance:                "r",
		TTLSecondsAfterFinished: &ttl,
	}

	if _, err := Job(strings.Repeat("a", 32), StageWorker, cfg); err == nil {
		t.Fatal("accepted negative retention")
	}
}

func TestJobArtifactConfiguration(t *testing.T) {
	id := strings.Repeat("a", 32)
	cfg := JobConfig{
		Image:    "i",
		Claim:    "c",
		Instance: "r",
		Artifacts: &ArtifactConfig{
			MaxBytes:         1024,
			RetentionSeconds: 60,
		},
	}

	job, err := Job(id, StageWorker, cfg)
	if err != nil {
		t.Fatal(err)
	}

	env := job.Spec.Template.Spec.Containers[0].Env
	if len(env) != 2 ||
		env[0].Name != config.EnvArtifactMaxBytes || env[0].Value != "1024" ||
		env[1].Name != config.EnvArtifactRetentionSeconds || env[1].Value != "60" {
		t.Fatalf("artifact environment = %v", env)
	}

	recorded, err := recordedJobConfig(map[string]interface{}{"jobConfig": jobConfigStatus(cfg, job)}, JobConfig{})
	if err != nil || recorded.Artifacts == nil || *recorded.Artifacts != *cfg.Artifacts {
		t.Fatalf("recorded config = %+v, %v", recorded, err)
	}

	cfg.Artifacts.MaxBytes = 0
	if _, err := Job(id, StageWorker, cfg); err == nil {
		t.Fatal("accepted zero artifact limit")
	}
}

func TestJobS3WorkloadIdentityAndMultiNodePlacement(t *testing.T) {
	id := strings.Repeat("b", 32)
	cfg := JobConfig{
		Image:                "example/kubereel:v1",
		Instance:             "cloud-rel",
		WorkerServiceAccount: "kubereel-worker",
		S3: &S3JobConfig{
			Bucket:         "kubereel-cloud-artifacts",
			Region:         "us-west-2",
			Prefix:         "/prod/",
			Endpoint:       "https://s3.us-west-2.amazonaws.com",
			ForcePathStyle: true,
		},
		Artifacts: &ArtifactConfig{
			MaxBytes:         2048,
			RetentionSeconds: 300,
		},
	}

	job, err := Job(id, StageWorker, cfg)
	if err != nil {
		t.Fatalf("Job(S3) error = %v", err)
	}

	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "kubereel-worker" {
		t.Fatalf("ServiceAccountName = %q, want kubereel-worker", pod.ServiceAccountName)
	}
	if *pod.AutomountServiceAccountToken {
		t.Fatal("worker pod unexpectedly automounted Kubernetes API token")
	}
	if len(pod.Volumes) != 0 || len(pod.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("S3 job mounted unexpected volumes: %+v / %+v", pod.Volumes, pod.Containers[0].VolumeMounts)
	}
	if pod.Affinity != nil {
		t.Fatalf("S3 job unexpectedly pinned to single node: %+v", pod.Affinity)
	}

	envMap := make(map[string]string, len(pod.Containers[0].Env))
	for _, item := range pod.Containers[0].Env {
		if item.ValueFrom != nil {
			t.Fatalf("workload identity S3 job injected static secret env var %q", item.Name)
		}
		envMap[item.Name] = item.Value
	}
	if envMap[config.EnvStorageType] != config.StorageTypeS3 ||
		envMap[config.EnvS3Bucket] != "kubereel-cloud-artifacts" ||
		envMap[config.EnvS3Region] != "us-west-2" ||
		envMap[config.EnvS3Prefix] != "prod" ||
		envMap[config.EnvS3ForcePathStyle] != "true" {
		t.Fatalf("unexpected S3 job env = %v", envMap)
	}

	status := jobConfigStatus(cfg, job)
	if _, hasClaim := status["claim"]; hasClaim {
		t.Fatalf("S3 jobConfigStatus unexpectedly set claim: %+v", status)
	}
	recorded, err := recordedJobConfig(map[string]interface{}{"jobConfig": status}, JobConfig{})
	if err != nil {
		t.Fatalf("recordedJobConfig(S3) error = %v", err)
	}
	rebuilt, err := Job(id, StageWorker, recorded)
	if err != nil {
		t.Fatalf("rebuild Job(S3) error = %v", err)
	}
	if err := validateStepPodSpec(&job.Spec.Template.Spec, &rebuilt.Spec.Template.Spec); err != nil {
		t.Fatalf("validateStepPodSpec(S3) mismatch = %v", err)
	}
	if err := validateStepContainerSpec(&job.Spec.Template.Spec.Containers[0], &rebuilt.Spec.Template.Spec.Containers[0]); err != nil {
		t.Fatalf("validateStepContainerSpec(S3) mismatch = %v", err)
	}

	// Verify optional Secret-backed S3 credentials for S3-compatible stores without workload identity.
	cfg.S3.Credentials = &S3SecretRefs{
		Name:               "minio-creds",
		AccessKeyIDKey:     "accessKeyId",
		SecretAccessKeyKey: "secretAccessKey",
	}
	secretJob, err := Job(id, StageRender, cfg)
	if err != nil {
		t.Fatalf("Job(S3 with Secret) error = %v", err)
	}
	secretStatus := jobConfigStatus(cfg, secretJob)
	recordedSecretCfg, err := recordedJobConfig(map[string]interface{}{"jobConfig": secretStatus}, JobConfig{})
	if err != nil || recordedSecretCfg.S3 == nil || recordedSecretCfg.S3.Credentials == nil {
		t.Fatalf("recorded S3 secret config = %+v, %v", recordedSecretCfg, err)
	}
	if recordedSecretCfg.S3.Credentials.Name != "minio-creds" {
		t.Fatalf("unexpected secret name = %q", recordedSecretCfg.S3.Credentials.Name)
	}
}
