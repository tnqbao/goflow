package workflow

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxDAGSteps         = 100
	maxStepNameRunes    = 63
	maxStepDependencies = 100
)

// DAGStep describes the fields needed to validate a VideoWorkflow step graph.
type DAGStep struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ValidateDAG checks that each dependency names a distinct step and the graph is acyclic.
func ValidateDAG(steps []DAGStep) error {
	if len(steps) == 0 || len(steps) > maxDAGSteps {
		return fmt.Errorf("%w: workflow must have 1 to %d steps", ErrInvalid, maxDAGSteps)
	}

	stepIndices, err := indexStepNames(steps)
	if err != nil {
		return err
	}

	dependents, indegree, err := buildDependencyGraph(steps, stepIndices)
	if err != nil {
		return err
	}

	if hasDependencyCycle(len(steps), dependents, indegree) {
		return fmt.Errorf("%w: workflow dependencies contain a cycle", ErrInvalid)
	}

	return nil
}

func indexStepNames(steps []DAGStep) (map[string]int, error) {
	indices := make(map[string]int, len(steps))

	for index, step := range steps {
		if strings.TrimSpace(step.Name) == "" || utf8.RuneCountInString(step.Name) > maxStepNameRunes {
			return nil, fmt.Errorf("%w: step name must have 1 to %d characters", ErrInvalid, maxStepNameRunes)
		}

		if _, exists := indices[step.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate step %q", ErrInvalid, step.Name)
		}

		indices[step.Name] = index
	}

	return indices, nil
}

func buildDependencyGraph(steps []DAGStep, stepIndices map[string]int) ([][]int, []int, error) {
	dependents := make([][]int, len(steps))
	indegree := make([]int, len(steps))

	for stepIndex, step := range steps {
		if len(step.DependsOn) > maxStepDependencies {
			return nil, nil, fmt.Errorf("%w: step %q has more than %d dependencies", ErrInvalid, step.Name, maxStepDependencies)
		}

		seenDeps := make(map[string]struct{}, len(step.DependsOn))
		for _, depName := range step.DependsOn {
			if _, exists := seenDeps[depName]; exists {
				return nil, nil, fmt.Errorf("%w: step %q repeats dependency %q", ErrInvalid, step.Name, depName)
			}
			seenDeps[depName] = struct{}{}

			depIndex, exists := stepIndices[depName]
			if !exists {
				return nil, nil, fmt.Errorf("%w: step %q references unknown dependency %q", ErrInvalid, step.Name, depName)
			}

			if depIndex == stepIndex {
				return nil, nil, fmt.Errorf("%w: step %q depends on itself", ErrInvalid, step.Name)
			}

			dependents[depIndex] = append(dependents[depIndex], stepIndex)
			indegree[stepIndex]++
		}
	}

	return dependents, indegree, nil
}

func hasDependencyCycle(stepCount int, dependents [][]int, indegree []int) bool {
	queue := make([]int, 0, stepCount)
	for index, count := range indegree {
		if count == 0 {
			queue = append(queue, index)
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

	return len(queue) != stepCount
}
