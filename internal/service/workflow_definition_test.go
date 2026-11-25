package service

import (
	"errors"
	"testing"

	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestWorkflowDefinitionGraphValidation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		steps   []map[string]any
		from    string
		invalid bool
	}{
		{"valid", []map[string]any{{"name": "generate"}, {"name": "render", "dependsOn": []string{"generate"}}}, "render", false},
		{"cycle", []map[string]any{{"name": "generate", "dependsOn": []string{"render"}}, {"name": "render", "dependsOn": []string{"generate"}}}, "render", true},
		{"unknown output", []map[string]any{{"name": "generate"}}, "render", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorkflowSpec(map[string]any{"steps": tt.steps, "outputs": []map[string]any{{"name": "film", "from": tt.from}}})
			if tt.invalid && !errors.Is(err, workflow.ErrInvalid) {
				t.Fatalf("expected validation error, got %v", err)
			}
			if !tt.invalid && err != nil {
				t.Fatal(err)
			}
		})
	}
}
