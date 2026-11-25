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
