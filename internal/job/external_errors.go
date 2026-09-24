package job

import "errors"

var (
	ErrExternalExecutionIDRequired = errors.New("execution_id is required")
	ErrExternalAgentWorkloadRequired = errors.New("workload.type must be agent")
	ErrExternalGoalRequired = errors.New("input.goal is required")
)
