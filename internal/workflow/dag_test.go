package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateDAG(t *testing.T) {
	tests := []struct {
		name  string
		steps []DAGStep
		want  string
	}{
		{name: "single step", steps: []DAGStep{{Name: "input"}}},
		{name: "independent steps", steps: []DAGStep{{Name: "a"}, {Name: "b"}}},
		{name: "diamond and disconnected step", steps: []DAGStep{
			{Name: "output", DependsOn: []string{"left", "right"}},
			{Name: "right", DependsOn: []string{"root"}},
			{Name: "root"}, {Name: "left", DependsOn: []string{"root"}}, {Name: "other"},
		}},
		{name: "empty", want: "1 to 100"},
		{name: "empty name", steps: []DAGStep{{Name: " "}}, want: "step name"},
		{name: "long name", steps: []DAGStep{{Name: strings.Repeat("a", 64)}}, want: "step name"},
		{name: "duplicate name", steps: []DAGStep{{Name: "a"}, {Name: "a"}}, want: `duplicate step "a"`},
		{name: "missing dependency", steps: []DAGStep{{Name: "a", DependsOn: []string{"missing"}}}, want: `unknown dependency "missing"`},
		{name: "empty dependency", steps: []DAGStep{{Name: "a", DependsOn: []string{""}}}, want: "unknown dependency"},
		{name: "duplicate dependency", steps: []DAGStep{{Name: "a"}, {Name: "b", DependsOn: []string{"a", "a"}}}, want: "repeats dependency"},
		{name: "self dependency", steps: []DAGStep{{Name: "a", DependsOn: []string{"a"}}}, want: "depends on itself"},
		{name: "cycle", steps: []DAGStep{{Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"a"}}}, want: "cycle"},
		{name: "cycle in disconnected graph", steps: []DAGStep{{Name: "ok"}, {Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"c"}}, {Name: "c", DependsOn: []string{"a"}}}, want: "cycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDAG(tt.steps)
			if tt.want == "" && err != nil {
				t.Fatalf("ValidateDAG: %v", err)
			}
			if tt.want == "" {
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateDAG error = %v, want ErrInvalid containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateDAGLimits(t *testing.T) {
	steps := make([]DAGStep, 100)
	for i := range steps {
		steps[i].Name = fmt.Sprintf("step-%d", i)
		if i > 0 {
			steps[i].DependsOn = []string{steps[i-1].Name}
		}
	}
	if err := ValidateDAG(steps); err != nil {
		t.Fatalf("100-step chain: %v", err)
	}
	if err := ValidateDAG(append(steps, DAGStep{Name: "extra"})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("101 steps: %v", err)
	}
	steps[0].DependsOn = make([]string, 101)
	if err := ValidateDAG(steps); !errors.Is(err, ErrInvalid) {
		t.Fatalf("101 dependencies: %v", err)
	}
}

func TestValidateDAGWorkflowSnapshotJSON(t *testing.T) {
	var snapshot struct {
		Steps []DAGStep `json:"steps"`
	}
	data := []byte(`{"steps":[{"name":"scene","type":"image","providerRef":"mock"},{"name":"output","type":"output","dependsOn":["scene"]}]}`)
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDAG(snapshot.Steps); err != nil {
		t.Fatalf("snapshot graph: %v", err)
	}
}
