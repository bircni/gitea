// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"fmt"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/modules/actions/jobparser"
	"gitea.dev/modules/json"
	"gitea.dev/modules/log"
	api "gitea.dev/modules/structs"
)

func getWorkflowDispatchInputsFromRun(run *actions_model.ActionRun) (map[string]any, error) {
	if run.Event != "workflow_dispatch" {
		return map[string]any{}, nil
	}
	var payload api.WorkflowDispatchPayload
	if err := json.Unmarshal([]byte(run.EventPayload), &payload); err != nil {
		return nil, err
	}
	return payload.Inputs, nil
}

// getInputsForJob returns the `inputs.*` top-level expression context for a job's evaluation.
//   - For top-level jobs, it falls back to the run's dispatch inputs (empty for non-dispatch events)
//   - For reusable workflow children (and nested callers), this is the direct parent caller's CallPayload.Inputs
func getInputsForJob(ctx context.Context, run *actions_model.ActionRun, job *actions_model.ActionRunJob) (map[string]any, error) {
	if job.ParentJobID == 0 {
		return getWorkflowDispatchInputsFromRun(run)
	}

	caller, err := actions_model.GetRunJobByRunAndID(ctx, run.ID, job.ParentJobID)
	if err != nil {
		return nil, fmt.Errorf("load caller job %d: %w", job.ParentJobID, err)
	}
	if caller.CallPayload == "" {
		// should not happen - a child job cannot reach this point if its caller's CallPayload hasn't been evaluated
		return map[string]any{}, nil
	}
	var p api.WorkflowCallPayload
	if err := json.Unmarshal([]byte(caller.CallPayload), &p); err != nil {
		return nil, fmt.Errorf("decode caller %d payload: %w", caller.ID, err)
	}
	if p.Inputs == nil {
		return map[string]any{}, nil
	}
	return p.Inputs, nil
}

// EvaluateJobIfForDisplay evaluates a job's `if:` on demand for display purposes only (read-only).
// It is used by the job view for jobs whose `if:` is not evaluated server-side at dispatch time
// (jobs without `needs`), so it can show the result without changing any dispatch behavior.
// It returns ok=false when the result cannot be determined yet (e.g. needs not finished, or expression error).
func EvaluateJobIfForDisplay(ctx context.Context, job *actions_model.ActionRunJob) (result, ok bool) {
	if err := job.LoadRun(ctx); err != nil {
		log.Error("EvaluateJobIfForDisplay LoadRun failed: job: %d, err: %v", job.ID, err)
		return false, false
	}
	vars, err := actions_model.GetVariablesOfRun(ctx, job.Run)
	if err != nil {
		log.Error("EvaluateJobIfForDisplay GetVariablesOfRun failed: job: %d, err: %v", job.ID, err)
		return false, false
	}
	// allNeedsSucceed only matters for the implicit success() of an empty `if:`; a job reaching
	// this display path while still pending is treated as if its needs (if any) have succeeded.
	res, err := evaluateJobIf(ctx, job.Run, nil, job, vars, true)
	if err != nil {
		return false, false
	}
	return res, true
}

// evaluateJobIf evaluates a job's `if:`
func evaluateJobIf(ctx context.Context, run *actions_model.ActionRun, attempt *actions_model.ActionRunAttempt, job *actions_model.ActionRunJob, vars map[string]string, allNeedsSucceed bool) (bool, error) {
	parsedJob, err := job.ParseJob()
	if err != nil {
		return false, err
	}
	// Empty `if:` reduces to implicit `success()` - true iff every need finished as Success.
	if len(parsedJob.If.Value) == 0 {
		return allNeedsSucceed, nil
	}
	jobResults, err := findJobNeedsAndFillJobResults(ctx, job)
	if err != nil {
		return false, err
	}
	inputs, err := getInputsForJob(ctx, run, job)
	if err != nil {
		return false, err
	}
	gitCtx := GenerateGiteaContext(ctx, run, attempt, job)
	return jobparser.EvaluateJobIfExpression(job.JobID, parsedJob, gitCtx, jobResults, vars, inputs)
}

func findJobNeedsAndFillJobResults(ctx context.Context, job *actions_model.ActionRunJob) (map[string]*jobparser.JobResult, error) {
	taskNeeds, err := FindTaskNeeds(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("find task needs: %w", err)
	}
	jobResults := make(map[string]*jobparser.JobResult, len(taskNeeds))
	for jobID, taskNeed := range taskNeeds {
		jobResult := &jobparser.JobResult{
			Result:  taskNeed.Result.String(),
			Outputs: taskNeed.Outputs,
		}
		jobResults[jobID] = jobResult
	}
	jobResults[job.JobID] = &jobparser.JobResult{
		Needs: job.Needs,
	}
	return jobResults, nil
}
