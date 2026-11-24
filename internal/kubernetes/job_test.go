package kubernetes

import (
	"strings"
	"testing"
)

func TestJobRespectsStorageAndStages(t *testing.T) {
	id := strings.Repeat("a", 32)
	cfg := JobConfig{Image: "example/kubereel:v1", Claim: "data", Instance: "test"}
	for _, stage := range []string{"worker", "render"} {
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
	job, err := Job(id, "worker", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Template.Spec.Affinity != nil {
		t.Fatal("shared storage unexpectedly pins to a node")
	}
	ttl := int32(30)
	cfg.TTLSecondsAfterFinished = &ttl
	job, err = Job(id, "worker", cfg)
	if err != nil || *job.Spec.TTLSecondsAfterFinished != ttl {
		t.Fatalf("Job retention: %v, %v", job, err)
	}
}

func TestJobRejectsInvalidInputs(t *testing.T) {
	valid := strings.Repeat("a", 32)
	for _, tt := range []struct {
		id, stage string
		cfg       JobConfig
	}{
		{"../id", "worker", JobConfig{Image: "i", Claim: "c", Instance: "r"}},
		{valid, "unknown", JobConfig{Image: "i", Claim: "c", Instance: "r"}},
		{valid, "worker", JobConfig{}},
	} {
		if _, err := Job(tt.id, tt.stage, tt.cfg); err == nil {
			t.Fatalf("accepted %+v", tt)
		}
	}
}

func TestJobRejectsNegativeRetention(t *testing.T) {
	ttl := int32(-1)
	_, err := Job(strings.Repeat("a", 32), "worker", JobConfig{Image: "i", Claim: "c", Instance: "r", TTLSecondsAfterFinished: &ttl})
	if err == nil {
		t.Fatal("accepted negative retention")
	}
}
