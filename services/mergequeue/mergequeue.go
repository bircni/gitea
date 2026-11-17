// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	"gitea.dev/modules/globallock"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/log"
	"gitea.dev/modules/process"
	"gitea.dev/modules/queue"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	notify_service "gitea.dev/services/notify"
	pull_service "gitea.dev/services/pull"
	repo_service "gitea.dev/services/repository"
)

// Reasons of the merge_group destroyed event, named like GitHub's
const (
	DestroyedMerged      = "merged"
	DestroyedInvalidated = "invalidated"
	DestroyedDequeued    = "dequeued"
)

var branchQueue *queue.WorkerPoolQueue[string]

// Init starts the queue that processes the merge queues of branches
func Init(ctx context.Context) error {
	notify_service.RegisterNotifier(&notifier{})
	branchQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "merge_queue", handleBranches)
	if branchQueue == nil {
		return errors.New("unable to create merge_queue queue")
	}
	go graceful.GetManager().RunWithCancel(branchQueue)
	return CheckAllQueues(ctx)
}

// CheckAllQueues schedules every non-empty queue, used on start and to enforce timeouts and wait times
func CheckAllQueues(ctx context.Context) error {
	branches, err := pull_model.GetMergeQueueBranches(ctx)
	if err != nil {
		return err
	}
	for _, b := range branches {
		addToQueue(b.RepoID, b.BaseBranch)
	}
	return nil
}

// addToQueue never blocks the caller: with an immediate queue the merge's own push hooks would otherwise wait for the branch lock held by the merge
func addToQueue(repoID int64, branch string) {
	go func() {
		if err := branchQueue.Push(fmt.Sprintf("%d:%s", repoID, branch)); err != nil && !errors.Is(err, queue.ErrAlreadyInQueue) {
			log.Error("Unable to push merge queue of repo %d branch %s: %v", repoID, branch, err)
		}
	}()
}

func handleBranches(items ...string) []string {
	for _, item := range items {
		idStr, branch, _ := strings.Cut(item, ":")
		repoID, _ := strconv.ParseInt(idStr, 10, 64)
		ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().HammerContext(), "merge queue "+item)
		err := globallock.LockAndDo(ctx, lockKey(repoID, branch), func(ctx context.Context) error {
			return processQueue(ctx, repoID, branch)
		})
		if err != nil {
			log.Error("Merge queue of repo %d branch %s: %v", repoID, branch, err)
		}
		finished()
	}
	return nil
}

func lockKey(repoID int64, branch string) string {
	return fmt.Sprintf("merge_queue_%d_%s", repoID, branch)
}

type queueState struct {
	repo    *repo_model.Repository
	gitRepo *git.Repository
	pb      *git_model.ProtectedBranch
	cfg     git_model.MergeQueueConfig
	branch  string
	entries []*pull_model.MergeQueueEntry
	prs     map[int64]*issues_model.PullRequest
}

// processQueue advances the queue of a branch by one step: drop stale entries, evaluate checks, merge, build
func processQueue(ctx context.Context, repoID int64, branch string) error {
	entries, err := pull_model.GetMergeQueueEntries(ctx, repoID, branch)
	if err != nil || len(entries) == 0 {
		return err
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return err
	}
	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, repoID, branch)
	if err != nil {
		return err
	}
	gitRepo, closer, err := git.RepositoryFromContextOrOpen(ctx, repo)
	if err != nil {
		return err
	}
	defer closer.Close()

	s := &queueState{repo: repo, gitRepo: gitRepo, pb: pb, branch: branch, prs: map[int64]*issues_model.PullRequest{}}
	if pb == nil || !pb.RequireMergeQueue {
		for _, e := range entries {
			if err := s.removeEntry(ctx, e, issues_model.MergeQueueRemovedNotMergeable); err != nil {
				return err
			}
		}
		return nil
	}
	s.cfg = pb.MergeQueue.Normalized()

	if err := s.dropStaleEntries(ctx, entries); err != nil {
		return err
	}
	baseSHA, err := gitRepo.GetBranchCommitID(ctx, branch)
	if err != nil {
		return err
	}
	if err := s.invalidateBrokenChain(ctx, baseSHA); err != nil {
		return err
	}
	if err := s.evaluateChecks(ctx); err != nil {
		return err
	}
	if merged, err := s.mergeReadyEntries(ctx); err != nil || merged {
		// the push to the base branch schedules the next step
		return err
	}
	return s.buildEntries(ctx, baseSHA)
}

func (s *queueState) loadPull(ctx context.Context, e *pull_model.MergeQueueEntry) (*issues_model.PullRequest, error) {
	if pr, ok := s.prs[e.PullID]; ok {
		return pr, nil
	}
	pr, err := issues_model.GetPullRequestByID(ctx, e.PullID)
	if err != nil {
		return nil, err
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, err
	}
	pr.BaseRepo = s.repo
	s.prs[e.PullID] = pr
	return pr, nil
}

func (s *queueState) enqueuer(ctx context.Context, e *pull_model.MergeQueueEntry) *user_model.User {
	_, u, err := user_model.GetPossibleUserByID(ctx, e.EnqueuerID)
	if err != nil {
		return user_model.NewGhostUser()
	}
	return u
}

// dropStaleEntries removes closed, merged and retargeted pull requests and those with new commits, as GitHub does
func (s *queueState) dropStaleEntries(ctx context.Context, entries []*pull_model.MergeQueueEntry) error {
	for _, e := range entries {
		pr, err := s.loadPull(ctx, e)
		if issues_model.IsErrPullRequestNotExist(err) {
			if _, err := pull_model.DeleteMergeQueueEntry(ctx, e.PullID); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if pr.HasMerged || pr.Issue.IsClosed || pr.BaseBranch != s.branch {
			if err := s.removeEntry(ctx, e, ""); err != nil {
				return err
			}
			continue
		}
		headSHA, err := s.gitRepo.GetRefCommitID(ctx, pr.GetGitHeadRefName())
		if err != nil {
			return err
		}
		if headSHA != e.HeadSHA {
			if err := s.removeEntry(ctx, e, issues_model.MergeQueueRemovedHeadChanged); err != nil {
				return err
			}
			continue
		}
		s.entries = append(s.entries, e)
	}
	return nil
}

// invalidateBrokenChain discards queue commits that are no longer built on the base branch and the entries before them
func (s *queueState) invalidateBrokenChain(ctx context.Context, baseSHA string) error {
	expected := baseSHA
	broken := false
	for _, e := range s.entries {
		if !e.IsBuilt() {
			broken = true // later entries must be built on this one first
			continue
		}
		if broken || e.BaseSHA != expected {
			broken = true
			if err := s.invalidateEntry(ctx, e); err != nil {
				return err
			}
			continue
		}
		expected = e.GroupHeadSHA
	}
	return nil
}

func (s *queueState) evaluateChecks(ctx context.Context) error {
	timeout := time.Duration(s.cfg.CheckResponseTimeoutMinutes) * time.Minute
	for _, e := range s.entries {
		if e.State != pull_model.MergeQueueStateAwaitingChecks {
			continue
		}
		state, err := pull_service.GetMergeQueueCommitStatusState(ctx, s.repo, s.pb, e.GroupHeadSHA)
		if err != nil {
			return err
		}
		switch {
		case state.IsSuccess():
			e.State = pull_model.MergeQueueStateMergeable
		case state == commitstatus.CommitStatusFailure || state == commitstatus.CommitStatusError:
			e.State = pull_model.MergeQueueStateUnmergeable
		case e.ChecksStartedUnix.AddDuration(timeout) <= timeutil.TimeStampNow():
			e.State = pull_model.MergeQueueStateUnmergeable
		default:
			continue
		}
		if err := pull_model.UpdateMergeQueueEntry(ctx, e); err != nil {
			return err
		}
	}
	return s.removeFailedEntries(ctx)
}

// removeFailedEntries drops failed entries and rebuilds the ones after them.
// With HEADGREEN a failed entry stays while a later queue commit, which contains its changes, may still pass.
func (s *queueState) removeFailedEntries(ctx context.Context) error {
	headGreen := s.cfg.GroupingStrategy == git_model.MergeQueueGroupingHeadGreen
	for i := 0; i < len(s.entries); i++ {
		e := s.entries[i]
		if e.State != pull_model.MergeQueueStateUnmergeable {
			continue
		}
		if headGreen && s.hasPendingOrPassingAfter(i) {
			continue
		}
		reason := issues_model.MergeQueueRemovedChecksFailed
		if state, err := pull_service.GetMergeQueueCommitStatusState(ctx, s.repo, s.pb, e.GroupHeadSHA); err == nil && state.IsPending() {
			reason = issues_model.MergeQueueRemovedChecksTimeout
		}
		if err := s.removeEntry(ctx, e, reason); err != nil {
			return err
		}
		for _, next := range s.entries[i+1:] {
			if next.IsBuilt() {
				if err := s.invalidateEntry(ctx, next); err != nil {
					return err
				}
			}
		}
		s.entries = append(s.entries[:i], s.entries[i+1:]...)
		i--
	}
	return nil
}

func (s *queueState) hasPendingOrPassingAfter(i int) bool {
	for _, e := range s.entries[i+1:] {
		if e.State == pull_model.MergeQueueStateAwaitingChecks || e.State == pull_model.MergeQueueStateMergeable {
			return true
		}
	}
	return false
}

// mergeReadyEntries fast-forwards the base branch to the furthest passing queue commit allowed by the group size settings
func (s *queueState) mergeReadyEntries(ctx context.Context) (bool, error) {
	last := -1
	for i, e := range s.entries {
		if i >= s.cfg.MaxEntriesToMerge || !e.IsBuilt() {
			break
		}
		if e.State == pull_model.MergeQueueStateMergeable {
			last = i
		} else if s.cfg.GroupingStrategy != git_model.MergeQueueGroupingHeadGreen {
			break
		}
	}
	if last < 0 {
		return false, nil
	}
	waited := s.entries[0].CreatedUnix.AddDuration(time.Duration(s.cfg.MinEntriesToMergeWaitMinutes)*time.Minute) <= timeutil.TimeStampNow()
	if last+1 < s.cfg.MinEntriesToMerge && !waited {
		return false, nil
	}

	group := s.entries[:last+1]
	prs := make([]*issues_model.PullRequest, 0, len(group))
	commitIDs := make([]string, 0, len(group))
	for _, e := range group {
		pr, err := s.loadPull(ctx, e)
		if err != nil {
			return false, err
		}
		prs = append(prs, pr)
		commitIDs = append(commitIDs, e.GroupHeadSHA)
	}
	doer := s.enqueuer(ctx, group[len(group)-1])
	if err := pull_service.MergeQueuePullRequests(ctx, doer, s.repo, s.branch, prs, commitIDs); err != nil {
		return false, err
	}

	for i, e := range group {
		if _, err := pull_model.DeleteMergeQueueEntry(ctx, e.PullID); err != nil {
			log.Error("DeleteMergeQueueEntry: %v", err)
		}
		s.destroyGroup(ctx, e, DestroyedMerged)
		deleteBranchAfterMerge(ctx, s.enqueuer(ctx, e), prs[i])
	}
	s.entries = s.entries[last+1:]
	return true, nil
}

func deleteBranchAfterMerge(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest) {
	shouldDelete, err := pull_service.ShouldDeleteBranchAfterMerge(ctx, nil, pr.BaseRepo, pr)
	if err != nil {
		log.Error("ShouldDeleteBranchAfterMerge: %v", err)
	} else if shouldDelete {
		if err := repo_service.DeleteBranchAfterMerge(ctx, doer, pr.ID, nil); err != nil {
			log.Error("DeleteBranchAfterMerge: %v", err)
		}
	}
}

// buildEntries creates queue commits until the build concurrency is reached, each on top of the one before it
func (s *queueState) buildEntries(ctx context.Context, baseSHA string) error {
	built := 0
	base := baseSHA
	for i := 0; i < len(s.entries); i++ {
		e := s.entries[i]
		if e.IsBuilt() {
			built++
			base = e.GroupHeadSHA
			continue
		}
		if built >= s.cfg.MaxEntriesToBuild {
			break
		}
		pr, err := s.loadPull(ctx, e)
		if err != nil {
			return err
		}
		doer := s.enqueuer(ctx, e)
		ref := pull_model.MergeQueueGroupRef(s.branch, pr.Index, e.HeadSHA)
		sha, err := pull_service.CreateMergeQueueCommit(ctx, pr, doer, s.cfg.MergeMethod, base, e.HeadSHA, ref)
		if err != nil {
			if !isConflict(err) {
				return err
			}
			if err := s.removeEntry(ctx, e, issues_model.MergeQueueRemovedConflict); err != nil {
				return err
			}
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			i--
			continue
		}
		e.State = pull_model.MergeQueueStateAwaitingChecks
		e.BaseSHA, e.GroupHeadSHA, e.GroupRef = base, sha, ref
		e.ChecksStartedUnix = timeutil.TimeStampNow()
		if err := pull_model.UpdateMergeQueueEntry(ctx, e); err != nil {
			return err
		}
		notify_service.MergeGroupChecksRequested(ctx, doer, s.repo, e)
		built++
		base = sha
	}
	if built > 0 {
		// statuses may already exist or no checks may be required
		addToQueue(s.repo.ID, s.branch)
	}
	return nil
}

func isConflict(err error) bool {
	return pull_service.IsErrMergeConflicts(err) || pull_service.IsErrRebaseConflicts(err) ||
		pull_service.IsErrMergeUnrelatedHistories(err) || pull_service.IsErrSHADoesNotMatch(err)
}

// invalidateEntry discards the queue commit so the entry is built again on the new base
func (s *queueState) invalidateEntry(ctx context.Context, e *pull_model.MergeQueueEntry) error {
	s.destroyGroup(ctx, e, DestroyedInvalidated)
	e.State = pull_model.MergeQueueStateQueued
	e.BaseSHA, e.GroupHeadSHA, e.GroupRef, e.ChecksStartedUnix = "", "", "", 0
	return pull_model.UpdateMergeQueueEntry(ctx, e)
}

func (s *queueState) destroyGroup(ctx context.Context, e *pull_model.MergeQueueEntry, reason string) {
	if !e.IsBuilt() {
		return
	}
	if err := git.RemoveRef(ctx, s.repo, e.GroupRef); err != nil {
		log.Error("RemoveRef %s: %v", e.GroupRef, err)
	}
	notify_service.MergeGroupDestroyed(ctx, s.enqueuer(ctx, e), s.repo, e, reason)
}

// removeEntry takes the pull request out of the queue, a comment is only added for an open pull request
func (s *queueState) removeEntry(ctx context.Context, e *pull_model.MergeQueueEntry, reason issues_model.MergeQueueRemovalReason) error {
	return removeEntry(ctx, s.repo, e, s.enqueuer(ctx, e), reason)
}

func removeEntry(ctx context.Context, repo *repo_model.Repository, e *pull_model.MergeQueueEntry, doer *user_model.User, reason issues_model.MergeQueueRemovalReason) error {
	pr, err := issues_model.GetPullRequestByID(ctx, e.PullID)
	if err != nil && !issues_model.IsErrPullRequestNotExist(err) {
		return err
	}
	// the comment is written in the same transaction, so it exists once the entry is gone
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if deleted, err := pull_model.DeleteMergeQueueEntry(ctx, e.PullID); err != nil || !deleted {
			return util.Iif(err != nil, err, errEntryGone)
		}
		if reason == "" || pr == nil {
			return nil
		}
		_, err := issues_model.CreateMergeQueueComment(ctx, pr, doer, false, reason)
		return err
	})
	if errors.Is(err, errEntryGone) {
		return nil
	} else if err != nil {
		return err
	}
	if e.IsBuilt() {
		if err := git.RemoveRef(ctx, repo, e.GroupRef); err != nil {
			log.Error("RemoveRef %s: %v", e.GroupRef, err)
		}
		notify_service.MergeGroupDestroyed(ctx, doer, repo, e, DestroyedDequeued)
	}
	if pr != nil {
		notify_service.PullRequestMergeQueueChanged(ctx, doer, pr, false)
	}
	return nil
}

var errEntryGone = errors.New("merge queue entry is already removed")
