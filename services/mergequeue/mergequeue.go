// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package mergequeue implements a GitHub-style "merge queue": pull requests opt in to a per-branch
// FIFO queue; queued PRs are tested in batches by building a synthetic commit reflecting what
// sequentially merging/rebasing them would produce against the *current* base tip, and only merged
// for real once required checks pass against that synthetic commit (posted via `on: merge_group`
// Actions workflows, see modules/actions). On failure the batch is bisected to find the culprit PR(s).
package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	access_model "gitea.dev/models/perm/access"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/globallock"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/log"
	"gitea.dev/modules/process"
	"gitea.dev/modules/queue"
	notify_service "gitea.dev/services/notify"
	pull_service "gitea.dev/services/pull"
	repo_service "gitea.dev/services/repository"
)

var mergeQueueQueue *queue.WorkerPoolQueue[string]

// Init creates the merge queue worker and registers the commit-status notifier that drives it.
func Init() error {
	notify_service.RegisterNotifier(NewNotifier())

	mergeQueueQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "merge_queue_batch", handler)
	if mergeQueueQueue == nil {
		return errors.New("unable to create merge_queue_batch queue")
	}
	go graceful.GetManager().RunWithCancel(mergeQueueQueue)
	return nil
}

func mergeQueueLockKey(repoID int64, baseBranch string) string {
	return fmt.Sprintf("mergequeue-%d-%s", repoID, baseBranch)
}

func triggerBranch(repoID int64, baseBranch string) {
	if mergeQueueQueue == nil {
		return
	}
	item := fmt.Sprintf("%d_%s", repoID, baseBranch)
	if err := mergeQueueQueue.Push(item); err != nil && !errors.Is(err, queue.ErrAlreadyInQueue) {
		log.Error("Error adding %s to the merge_queue_batch queue: %v", item, err)
	}
}

// triggerBranchAfter schedules a re-check of the branch's queue after d, used to respect
// MergeQueueWaitMinutes when there are fewer than a full batch's worth of queued entries.
func triggerBranchAfter(repoID int64, baseBranch string, d time.Duration) {
	time.AfterFunc(d, func() { triggerBranch(repoID, baseBranch) })
}

// TriggerQueuedBranches wakes the merge-queue worker for every active queued branch in the repo
// that matches the given protection rule, used after enabling the queue in settings.
func TriggerQueuedBranches(ctx context.Context, repoID int64, match func(string) bool) {
	if mergeQueueQueue == nil {
		return
	}
	entries, err := pull_model.GetMergeQueueEntriesForRuleView(ctx, repoID, match, 0)
	if err != nil {
		log.Error("mergequeue: GetMergeQueueEntriesForRuleView: %v", err)
		return
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if !entry.IsActive() {
			continue
		}
		if _, ok := seen[entry.BaseBranch]; ok {
			continue
		}
		seen[entry.BaseBranch] = struct{}{}
		triggerBranch(repoID, entry.BaseBranch)
	}
}

func handler(items ...string) []string {
	for _, s := range items {
		repoID, baseBranch := parseQueueItem(s)
		processBranch(repoID, baseBranch)
	}
	return nil
}

// parseQueueItem splits a "<repoID>_<baseBranch>" queue item. A plain fmt.Sscanf pattern can't be
// used here because %s would greedily consume nothing before the first non-digit, and base branch
// names may themselves contain underscores.
func parseQueueItem(s string) (repoID int64, baseBranch string) {
	for i := range s {
		if s[i] == '_' {
			fmt.Sscanf(s[:i], "%d", &repoID)
			baseBranch = s[i+1:]
			return
		}
	}
	return 0, ""
}

func headCommitID(ctx context.Context, pr *issues_model.PullRequest) (string, error) {
	baseGitRepo, err := git.OpenRepository(ctx, pr.BaseRepo)
	if err != nil {
		return "", err
	}
	defer baseGitRepo.Close()
	return baseGitRepo.GetRefCommitID(ctx, pr.GetGitHeadRefName())
}

// IsEnabledForBranch reports whether the merge queue is enabled for a repo's base branch, and if so
// returns the governing ProtectedBranch rule.
func IsEnabledForBranch(ctx context.Context, repoID int64, baseBranch string) (bool, *git_model.ProtectedBranch, error) {
	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, repoID, baseBranch)
	if err != nil {
		return false, nil, err
	}
	if pb == nil || !pb.EnableMergeQueue {
		return false, nil, nil
	}
	return true, pb, nil
}

func processBranch(repoID int64, baseBranch string) {
	if repoID == 0 || baseBranch == "" {
		return
	}

	ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().HammerContext(),
		fmt.Sprintf("Process merge queue for repo[%d] branch[%s]", repoID, baseBranch))
	defer finished()

	releaser, err := globallock.Lock(ctx, mergeQueueLockKey(repoID, baseBranch))
	if err != nil {
		log.Error("mergequeue: lock.Lock(): %v", err)
		return
	}
	defer releaser()

	if active, err := pull_model.HasActiveBatch(ctx, repoID, baseBranch); err != nil {
		log.Error("mergequeue: HasActiveBatch: %v", err)
		return
	} else if active {
		// already testing something for this branch; the notifier re-triggers once it settles
		return
	}

	enabled, pb, err := IsEnabledForBranch(ctx, repoID, baseBranch)
	if err != nil {
		log.Error("mergequeue: IsEnabledForBranch: %v", err)
		return
	}
	if !enabled {
		return
	}
	// minBatchSize is how many queued entries must be available before a batch starts immediately;
	// maxBatchSize caps how many are tested together at once. If fewer than minBatchSize are queued,
	// the worker waits up to waitMinutes for more to arrive before starting with whatever is available.
	minBatchSize, maxBatchSize := 1, 1
	waitMinutes := 0
	if pb.MergeQueueMinBatchSize > 0 {
		minBatchSize = pb.MergeQueueMinBatchSize
	}
	if pb.MergeQueueMaxBatchSize > 0 {
		maxBatchSize = pb.MergeQueueMaxBatchSize
	}
	if maxBatchSize < minBatchSize {
		maxBatchSize = minBatchSize
	}
	waitMinutes = pb.MergeQueueWaitMinutes

	candidates, err := pull_model.GetQueuedEntriesForBranch(ctx, repoID, baseBranch, maxBatchSize)
	if err != nil {
		log.Error("mergequeue: GetQueuedEntriesForBranch: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	if len(candidates) < minBatchSize && waitMinutes > 0 {
		oldest := candidates[0]
		deadline := oldest.CreatedUnix.AsTime().Add(time.Duration(waitMinutes) * time.Minute)
		if remaining := time.Until(deadline); remaining > 0 {
			triggerBranchAfter(repoID, baseBranch, remaining)
			return
		}
	}

	// Filter out any candidate whose PR is no longer valid (closed/merged) or has moved (force-pushed)
	// since it was enqueued; the rest proceed as this round's batch.
	entries := make([]*pull_model.MergeQueueEntry, 0, len(candidates))
	prs := make([]*issues_model.PullRequest, 0, len(candidates))
	for _, entry := range candidates {
		pr, ok := validateQueuedEntry(ctx, entry)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		prs = append(prs, pr)
	}
	if len(entries) == 0 {
		triggerBranch(repoID, baseBranch)
		return
	}

	mergeStyle := entries[0].MergeStyle
	if pb.MergeQueueMergeStyle != "" {
		mergeStyle = repo_model.MergeStyle(pb.MergeQueueMergeStyle)
	}

	startBatch(ctx, repoID, baseBranch, mergeStyle, entries, prs)
}

// validateQueuedEntry loads and re-validates a queued entry's PR, evicting it (and returning ok=false)
// if it is no longer a valid queue candidate.
func validateQueuedEntry(ctx context.Context, entry *pull_model.MergeQueueEntry) (pr *issues_model.PullRequest, ok bool) {
	pr, err := issues_model.GetPullRequestByID(ctx, entry.PullID)
	if err != nil {
		log.Error("mergequeue: GetPullRequestByID[%d]: %v", entry.PullID, err)
		return nil, false
	}
	if err := pr.LoadIssue(ctx); err != nil {
		log.Error("mergequeue: LoadIssue: %v", err)
		return nil, false
	}
	if pr.HasMerged || pr.Issue.IsClosed {
		markRemoved(ctx, entry, "pull request was closed or merged while queued")
		return nil, false
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Error("mergequeue: LoadBaseRepo: %v", err)
		return nil, false
	}
	currentHeadCommitID, err := headCommitID(ctx, pr)
	if err != nil {
		log.Error("mergequeue: headCommitID: %v", err)
		return nil, false
	}
	if currentHeadCommitID != entry.HeadCommitID {
		markRemoved(ctx, entry, "pull request was updated while queued")
		return nil, false
	}
	return pr, true
}

// startBatch builds and pushes the synthetic preview commit for a set of entries and marks them
// Testing. It is used both for a fresh pop off the front of the queue and for a bisection sub-batch.
func startBatch(ctx context.Context, repoID int64, baseBranch string, mergeStyle repo_model.MergeStyle, entries []*pull_model.MergeQueueEntry, prs []*issues_model.PullRequest) {
	if len(entries) == 0 {
		return
	}

	baseGitRepo, err := git.OpenRepository(ctx, prs[0].BaseRepo)
	if err != nil {
		log.Error("mergequeue: OpenRepository: %v", err)
		return
	}
	baseCommitID, err := baseGitRepo.GetBranchCommitID(ctx, baseBranch)
	baseGitRepo.Close()
	if err != nil {
		log.Error("mergequeue: GetBranchCommitID: %v", err)
		return
	}

	batch, err := pull_model.CreateMergeQueueBatch(ctx, repoID, baseBranch, baseCommitID, mergeStyle, entries)
	if err != nil {
		log.Error("mergequeue: CreateMergeQueueBatch: %v", err)
		return
	}

	doer, err := user_model.GetUserByID(ctx, entries[0].EnqueuedByID)
	if err != nil {
		log.Error("mergequeue: GetUserByID[%d]: %v", entries[0].EnqueuedByID, err)
		return
	}

	previewCommitID, err := pull_service.BuildMergeQueueBatchPreviewCommit(ctx, prs, doer, mergeStyle, batch.ID)
	if err != nil {
		log.Info("mergequeue: batch %d could not be tested (%v)", batch.ID, err)
		failOrBisectBatch(ctx, batch, fmt.Sprintf("could not build a test merge: %v", err))
		return
	}

	batch.TempCommitID = previewCommitID
	batch.TempRefName = pull_service.MergeQueueTempRefName(baseBranch, batch.ID)
	batch.Status = pull_model.MergeQueueBatchStatusTesting
	if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "temp_commit_id", "temp_ref_name", "status"); err != nil {
		log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
	}
	// progress from here is driven by the commit-status notifier (see notify.go) watching TempCommitID
}

func markRemoved(ctx context.Context, entry *pull_model.MergeQueueEntry, reason string) {
	if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusRemoved, reason); err != nil {
		log.Error("mergequeue: MarkMergeQueueEntryStatus: %v", err)
	}
	pr, err := issues_model.GetPullRequestByID(ctx, entry.PullID)
	if err != nil {
		return
	}
	if _, err := issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, nil, reason); err != nil {
		log.Error("mergequeue: CreateMergeQueueComment: %v", err)
	}
}

// EnqueuePullRequestOptions carries the per-PR merge details to store on the queue entry, used at
// finalize time so a queued merge behaves like a normal one (custom title/message, branch deletion).
type EnqueuePullRequestOptions struct {
	MergeStyle             repo_model.MergeStyle
	Message                string
	DeleteBranchAfterMerge bool
}

// EnqueuePullRequest validates and adds a pull request to its base branch's merge queue.
func EnqueuePullRequest(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, opts EnqueuePullRequestOptions) error {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return err
	}

	perm, err := access_model.GetDoerRepoPermission(ctx, pr.BaseRepo, doer)
	if err != nil {
		return err
	}
	if err := pull_service.CheckPullMergeable(ctx, doer, &perm, pr, pull_service.MergeCheckTypeQueue, opts.MergeStyle, false); err != nil {
		return err
	}

	currentHeadCommitID, err := headCommitID(ctx, pr)
	if err != nil {
		return err
	}

	err = db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := pull_model.EnqueueMergeQueueEntry(ctx, pull_model.EnqueueMergeQueueEntryOptions{
			RepoID:                 pr.BaseRepoID,
			BaseBranch:             pr.BaseBranch,
			PullID:                 pr.ID,
			EnqueuedByID:           doer.ID,
			HeadCommitID:           currentHeadCommitID,
			MergeStyle:             opts.MergeStyle,
			Message:                opts.Message,
			DeleteBranchAfterMerge: opts.DeleteBranchAfterMerge,
		}); err != nil {
			return err
		}
		_, err := issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRAddedToMergeQueue, pr, doer, "")
		return err
	})
	if err != nil {
		return err
	}

	triggerBranch(pr.BaseRepoID, pr.BaseBranch)
	return nil
}

func userCanRemoveMergeQueueEntry(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, entry *pull_model.MergeQueueEntry) (bool, error) {
	if doer.ID == entry.EnqueuedByID {
		return true, nil
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return false, err
	}
	perm, err := access_model.GetDoerRepoPermission(ctx, pr.BaseRepo, doer)
	if err != nil {
		return false, err
	}
	return pull_service.IsUserAllowedToMerge(ctx, pr, perm, doer)
}

// RemoveFromMergeQueue removes a pull request's active merge queue entry, if any.
// The enqueuer or a user allowed to merge may remove it; others get ErrNoPermissionToMerge.
func RemoveFromMergeQueue(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		exists, entry, err := pull_model.GetActiveMergeQueueEntryByPullID(ctx, pr.ID)
		if err != nil || !exists {
			return err
		}
		allowed, err := userCanRemoveMergeQueueEntry(ctx, doer, pr, entry)
		if err != nil {
			return err
		}
		if !allowed {
			return pull_service.ErrNoPermissionToMerge
		}
		if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusRemoved, "removed from the merge queue"); err != nil {
			return err
		}
		_, err = issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, doer, "removed from the merge queue")
		return err
	})
}

// RemoveEntryByID removes a specific queue entry on a repo whose base branch is accepted by
// matchBranch (the governing protection rule's Match), for use by the queue management page.
// It is a no-op if the entry is not active or does not belong to that repo+rule.
func RemoveEntryByID(ctx context.Context, repoID, entryID int64, matchBranch func(string) bool) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		exists, entry, err := pull_model.GetMergeQueueEntryByRepoAndID(ctx, repoID, entryID)
		if err != nil || !exists || !entry.IsActive() || matchBranch == nil || !matchBranch(entry.BaseBranch) {
			return err
		}
		if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusRemoved, "removed from the merge queue by an administrator"); err != nil {
			return err
		}
		pr, err := issues_model.GetPullRequestByID(ctx, entry.PullID)
		if err != nil {
			return err
		}
		_, err = issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, nil, "removed from the merge queue by an administrator")
		return err
	})
}

// removeFromMergeQueueSystem is like RemoveFromMergeQueue but attributed to no particular doer; used
// by notifier hooks reacting to force-push/close/retarget events.
func removeFromMergeQueueSystem(ctx context.Context, pr *issues_model.PullRequest, reason string) {
	err := db.WithTx(ctx, func(ctx context.Context) error {
		removed, err := pull_model.RemoveMergeQueueEntryByPullID(ctx, pr.ID, reason)
		if err != nil || !removed {
			return err
		}
		_, err = issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, nil, reason)
		return err
	})
	if err != nil {
		log.Error("mergequeue: removeFromMergeQueueSystem: %v", err)
	}
}

func testingEntries(entries []*pull_model.MergeQueueEntry) []*pull_model.MergeQueueEntry {
	out := make([]*pull_model.MergeQueueEntry, 0, len(entries))
	for _, e := range entries {
		if e.Status == pull_model.MergeQueueEntryStatusTesting {
			out = append(out, e)
		}
	}
	return out
}

func bisectHalves(entries []*pull_model.MergeQueueEntry) (first, rest []*pull_model.MergeQueueEntry) {
	mid := len(entries) / 2
	return entries[:mid], entries[mid:]
}

func entryIDs(entries []*pull_model.MergeQueueEntry) []int64 {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return ids
}

func loadInFlightBatch(ctx context.Context, id int64) *pull_model.MergeQueueBatch {
	exists, batch, err := pull_model.GetMergeQueueBatchByID(ctx, id)
	if err != nil {
		log.Error("mergequeue: GetMergeQueueBatchByID: %v", err)
		return nil
	}
	if !exists || (batch.Status != pull_model.MergeQueueBatchStatusBuilding && batch.Status != pull_model.MergeQueueBatchStatusTesting) {
		return nil
	}
	return batch
}

func deleteBatchTempRef(ctx context.Context, batch *pull_model.MergeQueueBatch) {
	if batch.TempRefName == "" {
		return
	}
	repo, err := repo_model.GetRepositoryByID(ctx, batch.RepoID)
	if err != nil {
		log.Error("mergequeue: GetRepositoryByID[%d]: %v", batch.RepoID, err)
		return
	}
	if err := pull_service.DeleteMergeQueueTempRef(ctx, repo, batch.BaseBranch, batch.ID); err != nil {
		log.Error("mergequeue: DeleteMergeQueueTempRef: %v", err)
	}
}

func abandonBatchIfQueueDisabled(ctx context.Context, batch *pull_model.MergeQueueBatch, entries []*pull_model.MergeQueueEntry) bool {
	enabled, _, err := IsEnabledForBranch(ctx, batch.RepoID, batch.BaseBranch)
	if err != nil {
		log.Error("mergequeue: IsEnabledForBranch: %v", err)
		return true
	}
	if enabled {
		return false
	}
	if err := pull_model.RequeueMergeQueueEntries(ctx, entryIDs(testingEntries(entries))); err != nil {
		log.Error("mergequeue: RequeueMergeQueueEntries: %v", err)
	}
	batch.Status = pull_model.MergeQueueBatchStatusFailed
	if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
		log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
	}
	deleteBatchTempRef(ctx, batch)
	triggerBranch(batch.RepoID, batch.BaseBranch)
	return true
}

// scheduleUnderBranchLock runs fn holding the per-branch lock on a new goroutine. Callers on the
// commit-status notify path must not Lock in-place: startBatch holds the same key while pushing the
// preview ref, and that push's hook can deliver CreateCommitStatus synchronously.
func scheduleUnderBranchLock(repoID int64, baseBranch string, fn func(context.Context)) {
	go func() {
		ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().HammerContext(),
			fmt.Sprintf("Merge queue for repo[%d] branch[%s]", repoID, baseBranch))
		defer finished()
		releaser, err := globallock.Lock(ctx, mergeQueueLockKey(repoID, baseBranch))
		if err != nil {
			log.Error("mergequeue: lock.Lock(): %v", err)
			return
		}
		defer releaser()
		fn(ctx)
	}()
}

// failOrBisectBatch handles a batch that failed (either it could not be built, or its required checks
// failed). A single-entry batch is a direct eviction. A multi-entry batch starts only the first half
// as the next batch (rebuilt from the original base tip) and requeues the rest so the worker picks
// them up after that half settles; never two in-flight batches on the same branch.
func failOrBisectBatch(ctx context.Context, batch *pull_model.MergeQueueBatch, reason string) {
	batch = loadInFlightBatch(ctx, batch.ID)
	if batch == nil {
		return
	}

	entries, err := pull_model.GetMergeQueueEntriesByBatchID(ctx, batch.ID)
	if err != nil {
		log.Error("mergequeue: GetMergeQueueEntriesByBatchID: %v", err)
		return
	}
	if abandonBatchIfQueueDisabled(ctx, batch, entries) {
		return
	}

	batch.Status = pull_model.MergeQueueBatchStatusFailed
	if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
		log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
	}
	deleteBatchTempRef(ctx, batch)

	entries = testingEntries(entries)
	if len(entries) <= 1 {
		for _, entry := range entries {
			if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusFailed, reason); err != nil {
				log.Error("mergequeue: MarkMergeQueueEntryStatus: %v", err)
				continue
			}
			if pr, err := issues_model.GetPullRequestByID(ctx, entry.PullID); err == nil {
				if _, err := issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, nil, reason); err != nil {
					log.Error("mergequeue: CreateMergeQueueComment: %v", err)
				}
			}
		}
		triggerBranch(batch.RepoID, batch.BaseBranch)
		return
	}

	first, rest := bisectHalves(entries)
	if err := pull_model.RequeueMergeQueueEntries(ctx, entryIDs(rest)); err != nil {
		log.Error("mergequeue: RequeueMergeQueueEntries: %v", err)
		triggerBranch(batch.RepoID, batch.BaseBranch)
		return
	}

	prs := make([]*issues_model.PullRequest, 0, len(first))
	valid := make([]*pull_model.MergeQueueEntry, 0, len(first))
	for _, entry := range first {
		pr, ok := validateQueuedEntry(ctx, entry)
		if !ok {
			continue
		}
		prs = append(prs, pr)
		valid = append(valid, entry)
	}
	if len(valid) == 0 {
		triggerBranch(batch.RepoID, batch.BaseBranch)
		return
	}
	startBatch(ctx, batch.RepoID, batch.BaseBranch, batch.MergeStyle, valid, prs)
}

// finalizeBatch is called by the commit-status notifier once a batch's required checks all pass.
func finalizeBatch(ctx context.Context, batch *pull_model.MergeQueueBatch) {
	batch = loadInFlightBatch(ctx, batch.ID)
	if batch == nil {
		return
	}

	entries, err := pull_model.GetMergeQueueEntriesByBatchID(ctx, batch.ID)
	if err != nil {
		log.Error("mergequeue: GetMergeQueueEntriesByBatchID: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	if abandonBatchIfQueueDisabled(ctx, batch, entries) {
		return
	}

	testing := testingEntries(entries)
	if len(testing) != len(entries) {
		// composition changed while the batch was in flight (cancel/close/force-push); do not land
		// remaining PRs based on a test that included the dropped ones
		if err := pull_model.RequeueMergeQueueEntries(ctx, entryIDs(testing)); err != nil {
			log.Error("mergequeue: RequeueMergeQueueEntries: %v", err)
		}
		batch.Status = pull_model.MergeQueueBatchStatusFailed
		if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
			log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
		}
		deleteBatchTempRef(ctx, batch)
		triggerBranch(batch.RepoID, batch.BaseBranch)
		return
	}

	currentBaseTip, closeRepo, err := currentBaseBranchTip(ctx, batch, testing[0])
	if err != nil {
		log.Error("mergequeue: currentBaseBranchTip: %v", err)
		return
	}
	closeRepo()

	if currentBaseTip != batch.BaseCommitID {
		// base branch moved underneath the queue between test-pass and finalize: not the PRs' fault, requeue them
		if err := pull_model.RequeueMergeQueueEntries(ctx, entryIDs(testing)); err != nil {
			log.Error("mergequeue: RequeueMergeQueueEntries: %v", err)
		}
		batch.Status = pull_model.MergeQueueBatchStatusFailed
		if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
			log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
		}
		deleteBatchTempRef(ctx, batch)
		triggerBranch(batch.RepoID, batch.BaseBranch)
		return
	}

	for i, entry := range testing {
		if !mergeQueueEntryFinalize(ctx, entry, batch.MergeStyle) {
			if err := pull_model.RequeueMergeQueueEntries(ctx, entryIDs(testing[i:])); err != nil {
				log.Error("mergequeue: RequeueMergeQueueEntries: %v", err)
			}
			batch.Status = pull_model.MergeQueueBatchStatusFailed
			if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
				log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
			}
			deleteBatchTempRef(ctx, batch)
			triggerBranch(batch.RepoID, batch.BaseBranch)
			return
		}
	}

	deleteBatchTempRef(ctx, batch)

	batch.Status = pull_model.MergeQueueBatchStatusSucceeded
	if err := pull_model.UpdateMergeQueueBatch(ctx, batch, "status"); err != nil {
		log.Error("mergequeue: UpdateMergeQueueBatch: %v", err)
	}

	triggerBranch(batch.RepoID, batch.BaseBranch)
}

func mergeQueueEntryFinalize(ctx context.Context, entry *pull_model.MergeQueueEntry, mergeStyle repo_model.MergeStyle) bool {
	pr, err := issues_model.GetPullRequestByID(ctx, entry.PullID)
	if err != nil {
		log.Error("mergequeue: GetPullRequestByID[%d]: %v", entry.PullID, err)
		return false
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Error("mergequeue: LoadBaseRepo: %v", err)
		return false
	}
	doer, err := user_model.GetUserByID(ctx, entry.EnqueuedByID)
	if err != nil {
		log.Error("mergequeue: GetUserByID[%d]: %v", entry.EnqueuedByID, err)
		return false
	}
	perm, err := access_model.GetDoerRepoPermission(ctx, pr.BaseRepo, doer)
	if err != nil {
		log.Error("mergequeue: GetDoerRepoPermission: %v", err)
		return false
	}

	evict := func(reason string) {
		if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusFailed, reason); err != nil {
			log.Error("mergequeue: MarkMergeQueueEntryStatus: %v", err)
		}
		if _, err := issues_model.CreateMergeQueueComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, nil, reason); err != nil {
			log.Error("mergequeue: CreateMergeQueueComment: %v", err)
		}
	}

	if err := pull_service.CheckPullMergeable(ctx, doer, &perm, pr, pull_service.MergeCheckTypeQueue, mergeStyle, false); err != nil {
		log.Info("mergequeue: %-v is no longer mergeable at finalize time (%v), evicting", pr, err)
		evict(err.Error())
		return false
	}

	if err := pull_service.Merge(pr, doer, mergeStyle, "", entry.Message, true); err != nil {
		log.Error("mergequeue: Merge failed for %-v: %v", pr, err)
		evict(err.Error())
		return false
	}

	if err := pull_model.MarkMergeQueueEntryStatus(ctx, entry.ID, pull_model.MergeQueueEntryStatusSucceeded, ""); err != nil {
		log.Error("mergequeue: MarkMergeQueueEntryStatus: %v", err)
	}

	if deleteBranchAfterMerge, err := pull_service.ShouldDeleteBranchAfterMerge(ctx, &entry.DeleteBranchAfterMerge, pr.BaseRepo, pr); err != nil {
		log.Error("mergequeue: ShouldDeleteBranchAfterMerge: %v", err)
	} else if deleteBranchAfterMerge {
		if err := repo_service.DeleteBranchAfterMerge(ctx, doer, pr.ID, nil); err != nil {
			log.Error("mergequeue: DeleteBranchAfterMerge: %v", err)
		}
	}
	return true
}

// currentBaseBranchTip returns the base branch's current tip commit id, to detect drift against
// batch.BaseCommitID before finalizing.
func currentBaseBranchTip(ctx context.Context, batch *pull_model.MergeQueueBatch, anyEntry *pull_model.MergeQueueEntry) (tip string, closer func(), err error) {
	pr, err := issues_model.GetPullRequestByID(ctx, anyEntry.PullID)
	if err != nil {
		return "", func() {}, err
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return "", func() {}, err
	}
	gitRepo, err := git.OpenRepository(ctx, pr.BaseRepo)
	if err != nil {
		return "", func() {}, err
	}
	tip, err = gitRepo.GetBranchCommitID(ctx, batch.BaseBranch)
	if err != nil {
		gitRepo.Close()
		return "", func() {}, err
	}
	return tip, func() { gitRepo.Close() }, nil
}
