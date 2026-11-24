package workflow

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// DAGStep describes the fields needed to validate a VideoWorkflow step graph.
type DAGStep struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ValidateDAG checks that each dependency names a distinct step and the graph is acyclic.
func ValidateDAG(steps []DAGStep) error {
	if len(steps) == 0 || len(steps) > 100 {
		return fmt.Errorf("%w: workflow must have 1 to 100 steps", ErrInvalid)
	}

	indices := make(map[string]int, len(steps))
	for i, step := range steps {
		if strings.TrimSpace(step.Name) == "" || utf8.RuneCountInString(step.Name) > 63 {
			return fmt.Errorf("%w: step name must have 1 to 63 characters", ErrInvalid)
		}
		if _, exists := indices[step.Name]; exists {
			return fmt.Errorf("%w: duplicate step %q", ErrInvalid, step.Name)
		}
		indices[step.Name] = i
	}

	dependents := make([][]int, len(steps))
	indegree := make([]int, len(steps))
	for i, step := range steps {
		if len(step.DependsOn) > 100 {
			return fmt.Errorf("%w: step %q has more than 100 dependencies", ErrInvalid, step.Name)
		}
		seen := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, exists := seen[dependency]; exists {
				return fmt.Errorf("%w: step %q repeats dependency %q", ErrInvalid, step.Name, dependency)
			}
			seen[dependency] = struct{}{}
			dependencyIndex, exists := indices[dependency]
			if !exists {
				return fmt.Errorf("%w: step %q references unknown dependency %q", ErrInvalid, step.Name, dependency)
			}
			if dependencyIndex == i {
				return fmt.Errorf("%w: step %q depends on itself", ErrInvalid, step.Name)
			}
			dependents[dependencyIndex] = append(dependents[dependencyIndex], i)
			indegree[i]++
		}
	}

	queue := make([]int, 0, len(steps))
	for i, count := range indegree {
		if count == 0 {
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, dependent := range dependents[queue[head]] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if len(queue) != len(steps) {
		return fmt.Errorf("%w: workflow dependencies contain a cycle", ErrInvalid)
	}
	return nil
}
