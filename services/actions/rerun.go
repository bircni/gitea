// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"fmt"

	actions_model "code.gitea.io/gitea/models/actions"
	"code.gitea.io/gitea/models/db"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unit"
	"code.gitea.io/gitea/modules/container"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/util"
	notify_service "code.gitea.io/gitea/services/notify"

	"github.com/nektos/act/pkg/model"
	"go.yaml.in/yaml/v4"
	"xorm.io/builder"
)

func validateWorkflowRerunConfig(ctx context.Context, repo *repo_model.Repository, run *actions_model.ActionRun) error {
	cfgUnit := repo.MustGetUnit(ctx, unit.TypeActions)
	cfg := cfgUnit.ActionsConfig()
	if cfg.IsWorkflowDisabled(run.WorkflowID) {
		return util.NewInvalidArgumentErrorf("workflow %s is disabled", run.WorkflowID)
	}
	return nil
}

// GetFailedRerunJobs returns all failed jobs and their downstream dependent jobs that need to be rerun
func GetFailedRerunJobs(allJobs []*actions_model.ActionRunJob) []*actions_model.ActionRunJob {
	rerunJobIDSet := make(container.Set[int64])
	var jobsToRerun []*actions_model.ActionRunJob

	for _, job := range allJobs {
		if job.Status == actions_model.StatusFailure || job.Status == actions_model.StatusCancelled {
			for _, j := range GetAllRerunJobs(job, allJobs) {
				if !rerunJobIDSet.Contains(j.ID) {
					rerunJobIDSet.Add(j.ID)
					jobsToRerun = append(jobsToRerun, j)
				}
			}
		}
	}

	return jobsToRerun
}

// GetAllRerunJobs returns the target job and all jobs that transitively depend on it.
// Downstream jobs are included regardless of their current status.
func GetAllRerunJobs(job *actions_model.ActionRunJob, allJobs []*actions_model.ActionRunJob) []*actions_model.ActionRunJob {
	rerunJobs := []*actions_model.ActionRunJob{job}
	rerunJobsIDSet := make(container.Set[string])
	rerunJobsIDSet.Add(job.JobID)

	for {
		found := false
		for _, j := range allJobs {
			if rerunJobsIDSet.Contains(j.JobID) {
				continue
			}
			for _, need := range j.Needs {
				if rerunJobsIDSet.Contains(need) {
					found = true
					rerunJobs = append(rerunJobs, j)
					rerunJobsIDSet.Add(j.JobID)
					break
				}
			}
		}
		if !found {
			break
		}
	}

	return rerunJobs
}

// ValidateJobRerunEligible checks whether a target job can be rerun in the current run state.
// A need in StatusSkipped counts as non-success (same as failure or cancellation) unless the
// target job declares an "if" condition, in which case the runner may still schedule it.
func ValidateJobRerunEligible(run *actions_model.ActionRun, targetJob *actions_model.ActionRunJob, allJobs []*actions_model.ActionRunJob) error {
	if !targetJob.Status.IsDone() {
		return util.NewInvalidArgumentErrorf("job %s is not done", targetJob.JobID)
	}
	if !run.Status.IsDone() && targetJob.Status != actions_model.StatusFailure && targetJob.Status != actions_model.StatusCancelled {
		return util.NewInvalidArgumentErrorf("job %s can only be rerun while run is active when job status is failure or cancelled", targetJob.JobID)
	}

	jobsByWorkflowID := make(map[string]*actions_model.ActionRunJob, len(allJobs))
	for _, j := range allJobs {
		if _, ok := jobsByWorkflowID[j.JobID]; !ok {
			jobsByWorkflowID[j.JobID] = j
		}
	}

	targetHasIfCondition := false
	for _, needJobID := range targetJob.Needs {
		needJob, ok := jobsByWorkflowID[needJobID]
		if !ok {
			return util.NewInvalidArgumentErrorf("required job %s for %s was not found", needJobID, targetJob.JobID)
		}
		needStatus := needJob.Status
		if !needStatus.IsDone() {
			return util.NewInvalidArgumentErrorf("job %s requires %s to finish before rerun", targetJob.JobID, needJobID)
		}
		if needStatus != actions_model.StatusSuccess {
			if !targetHasIfCondition {
				targetHasIfCondition = targetJob.HasIfCondition()
			}
			if targetHasIfCondition {
				continue
			}
			return util.NewInvalidArgumentErrorf("job %s requires %s to succeed before rerun", targetJob.JobID, needJobID)
		}
	}

	return nil
}

// CanRerunJob reports whether a specific job is rerunnable for UI/API hinting.
func CanRerunJob(run *actions_model.ActionRun, targetJob *actions_model.ActionRunJob, allJobs []*actions_model.ActionRunJob) bool {
	return ValidateJobRerunEligible(run, targetJob, allJobs) == nil
}

// prepareRunRerun validates the run, resets its state, handles concurrency, persists the
// updated run, and fires a status-update notification.
// It returns isRunBlocked (true when the run itself is held by a concurrency group).
func prepareRunRerun(ctx context.Context, repo *repo_model.Repository, run *actions_model.ActionRun, jobs []*actions_model.ActionRunJob) (isRunBlocked bool, err error) {
	if !run.Status.IsDone() {
		return false, util.NewInvalidArgumentErrorf("this workflow run is not done")
	}
	if err := validateWorkflowRerunConfig(ctx, repo, run); err != nil {
		return false, err
	}

	// Reset run's timestamps and status.
	run.PreviousDuration = run.Duration()
	run.Started = 0
	run.Stopped = 0
	run.Status = actions_model.StatusWaiting

	vars, err := actions_model.GetVariablesOfRun(ctx, run)
	if err != nil {
		return false, fmt.Errorf("get run %d variables: %w", run.ID, err)
	}

	if run.RawConcurrency != "" {
		var rawConcurrency model.RawConcurrency
		if err := yaml.Unmarshal([]byte(run.RawConcurrency), &rawConcurrency); err != nil {
			return false, fmt.Errorf("unmarshal raw concurrency: %w", err)
		}

		if err := EvaluateRunConcurrencyFillModel(ctx, run, &rawConcurrency, vars, nil); err != nil {
			return false, err
		}

		run.Status, err = PrepareToStartRunWithConcurrency(ctx, run)
		if err != nil {
			return false, err
		}
	}

	if err := actions_model.UpdateRun(ctx, run, "started", "stopped", "previous_duration", "status", "concurrency_group", "concurrency_cancel"); err != nil {
		return false, err
	}

	if err := run.LoadAttributes(ctx); err != nil {
		return false, err
	}

	for _, job := range jobs {
		job.Run = run
	}

	notify_service.WorkflowRunStatusUpdate(ctx, run.Repo, run.TriggerUser, run)

	return run.Status == actions_model.StatusBlocked, nil
}

// rerunJobs resets each finished job in jobsToRerun. Jobs that are not in a terminal status
// (waiting, running, blocked, etc.) are left unchanged so an in-flight run can still rerun a
// failed upstream job while dependents are pending; see GetAllRerunJobs.
func rerunJobs(ctx context.Context, jobsToRerun []*actions_model.ActionRunJob, isRunBlocked bool) error {
	rerunJobIDs := make(container.Set[string])
	for _, j := range jobsToRerun {
		rerunJobIDs.Add(j.JobID)
	}

	for _, job := range jobsToRerun {
		if !job.Status.IsDone() {
			log.Debug("rerunJobs: skip reset for job %q (status=%s): not a terminal status", job.JobID, job.Status.String())
			continue
		}
		shouldBlockJob := isRunBlocked
		if !shouldBlockJob {
			for _, need := range job.Needs {
				if rerunJobIDs.Contains(need) {
					shouldBlockJob = true
					break
				}
			}
		}
		if err := rerunWorkflowJob(ctx, job, shouldBlockJob); err != nil {
			return err
		}
	}
	return nil
}

// RerunWorkflowRunJobs reruns the given jobs of a workflow run.
// jobsToRerun must include all jobs to be rerun (the target job and its transitively dependent jobs).
// A job is blocked (waiting for dependencies) if the run itself is blocked or if any of its
// needs are also being rerun.
func RerunWorkflowRunJobs(ctx context.Context, repo *repo_model.Repository, run *actions_model.ActionRun, jobsToRerun []*actions_model.ActionRunJob) error {
	if len(jobsToRerun) == 0 {
		return nil
	}

	isRunBlocked, err := prepareRunRerun(ctx, repo, run, jobsToRerun)
	if err != nil {
		return err
	}

	return rerunJobs(ctx, jobsToRerun, isRunBlocked)
}

// RerunWorkflowJobAndDependents reruns a target job and all of its downstream jobs.
func RerunWorkflowJobAndDependents(ctx context.Context, repo *repo_model.Repository, run *actions_model.ActionRun, targetJob *actions_model.ActionRunJob, allJobs []*actions_model.ActionRunJob) error {
	if err := ValidateJobRerunEligible(run, targetJob, allJobs); err != nil {
		return err
	}

	jobsToRerun := GetAllRerunJobs(targetJob, allJobs)
	if run.Status.IsDone() {
		return RerunWorkflowRunJobs(ctx, repo, run, jobsToRerun)
	}

	if err := validateWorkflowRerunConfig(ctx, repo, run); err != nil {
		return err
	}
	return rerunJobs(ctx, jobsToRerun, run.Status == actions_model.StatusBlocked)
}

func rerunWorkflowJob(ctx context.Context, job *actions_model.ActionRunJob, shouldBlock bool) error {
	status := job.Status
	if !status.IsDone() {
		return fmt.Errorf("rerunWorkflowJob: job %q has non-terminal status %s", job.JobID, status.String())
	}

	job.TaskID = 0
	job.Status = util.Iif(shouldBlock, actions_model.StatusBlocked, actions_model.StatusWaiting)
	job.Started = 0
	job.Stopped = 0
	job.ConcurrencyGroup = ""
	job.ConcurrencyCancel = false
	job.IsConcurrencyEvaluated = false

	if err := job.LoadRun(ctx); err != nil {
		return err
	}
	if err := job.Run.LoadAttributes(ctx); err != nil {
		return err
	}

	vars, err := actions_model.GetVariablesOfRun(ctx, job.Run)
	if err != nil {
		return fmt.Errorf("get run %d variables: %w", job.Run.ID, err)
	}

	if job.RawConcurrency != "" && !shouldBlock {
		if err := EvaluateJobConcurrencyFillModel(ctx, job.Run, job, vars, nil); err != nil {
			return fmt.Errorf("evaluate job concurrency: %w", err)
		}

		job.Status, err = PrepareToStartJobWithConcurrency(ctx, job)
		if err != nil {
			return err
		}
	}

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		updateCols := []string{"task_id", "status", "started", "stopped", "concurrency_group", "concurrency_cancel", "is_concurrency_evaluated"}
		_, err := actions_model.UpdateRunJob(ctx, job, builder.Eq{"status": status}, updateCols...)
		return err
	}); err != nil {
		return err
	}

	CreateCommitStatusForRunJobs(ctx, job.Run, job)
	notify_service.WorkflowJobStatusUpdate(ctx, job.Run.Repo, job.Run.TriggerUser, job, nil)
	return nil
}
