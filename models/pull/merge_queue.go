// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"fmt"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/timeutil"
)

type MergeQueueEntryStatus int

const (
	MergeQueueEntryStatusQueued    MergeQueueEntryStatus = iota // waiting for its batch to start
	MergeQueueEntryStatusTesting                                // part of an in-flight batch being tested
	MergeQueueEntryStatusSucceeded                              // batch succeeded, entry merged (terminal)
	MergeQueueEntryStatusFailed                                 // batch/bisection determined this PR is the culprit (terminal, PR evicted)
	MergeQueueEntryStatusRemoved                                // manually dequeued, PR closed/updated, or base changed underneath it (terminal)
)

// MergeQueueEntry represents a pull request that is waiting in (or has passed through) a
// repository's merge queue for a given base branch.
type MergeQueueEntry struct {
	ID                     int64                 `xorm:"pk autoincr"`
	RepoID                 int64                 `xorm:"INDEX NOT NULL"`
	BaseBranch             string                `xorm:"VARCHAR(255) INDEX NOT NULL"`
	PullID                 int64                 `xorm:"INDEX NOT NULL"`
	Position               int64                 `xorm:"INDEX NOT NULL"`
	BatchID                int64                 `xorm:"INDEX"`
	Status                 MergeQueueEntryStatus `xorm:"INDEX NOT NULL"`
	HeadCommitID           string                `xorm:"VARCHAR(64) NOT NULL"`
	EnqueuedByID           int64                 `xorm:"NOT NULL"`
	MergeStyle             repo_model.MergeStyle `xorm:"VARCHAR(30) NOT NULL"`
	Message                string                `xorm:"LONGTEXT"`
	DeleteBranchAfterMerge bool
	RemovalReason          string             `xorm:"TEXT"`
	CreatedUnix            timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix            timeutil.TimeStamp `xorm:"updated"`
}

// TableName returns the database table name for xorm
func (MergeQueueEntry) TableName() string {
	return "pull_merge_queue_entry"
}

// IsActive reports whether the entry is still occupying a slot in the queue (not terminal).
func (e *MergeQueueEntry) IsActive() bool {
	return e.Status == MergeQueueEntryStatusQueued || e.Status == MergeQueueEntryStatusTesting
}

type MergeQueueBatchStatus int

const (
	MergeQueueBatchStatusBuilding MergeQueueBatchStatus = iota // synthetic ref being constructed
	MergeQueueBatchStatusTesting                               // pushed, waiting on required checks
	MergeQueueBatchStatusSucceeded
	MergeQueueBatchStatusFailed
)

// MergeQueueBatch groups one or more MergeQueueEntry rows that are being tested together
// against a single synthetic combined commit.
type MergeQueueBatch struct {
	ID           int64                 `xorm:"pk autoincr"`
	RepoID       int64                 `xorm:"INDEX NOT NULL"`
	BaseBranch   string                `xorm:"VARCHAR(255) NOT NULL"`
	BaseCommitID string                `xorm:"VARCHAR(64) NOT NULL"`
	TempCommitID string                `xorm:"VARCHAR(64) INDEX"`
	TempRefName  string                `xorm:"VARCHAR(255)"`
	MergeStyle   repo_model.MergeStyle `xorm:"VARCHAR(30) NOT NULL"`
	Status       MergeQueueBatchStatus `xorm:"INDEX NOT NULL"`
	CreatedUnix  timeutil.TimeStamp    `xorm:"created"`
	UpdatedUnix  timeutil.TimeStamp    `xorm:"updated"`
}

// TableName returns the database table name for xorm
func (MergeQueueBatch) TableName() string {
	return "pull_merge_queue_batch"
}

func init() {
	db.RegisterModel(new(MergeQueueEntry))
	db.RegisterModel(new(MergeQueueBatch))
}

// ErrAlreadyInMergeQueue is returned when a pull request already has an active merge queue entry.
type ErrAlreadyInMergeQueue struct {
	PullID int64
}

func (err ErrAlreadyInMergeQueue) Error() string {
	return fmt.Sprintf("pull request is already in the merge queue [pull_id: %d]", err.PullID)
}

// IsErrAlreadyInMergeQueue checks if an error is a ErrAlreadyInMergeQueue.
func IsErrAlreadyInMergeQueue(err error) bool {
	_, ok := err.(ErrAlreadyInMergeQueue)
	return ok
}

// NextPosition returns the next FIFO position for a new entry on the given repo+branch queue.
func NextPosition(ctx context.Context, repoID int64, baseBranch string) (int64, error) {
	var maxPosition int64
	has, err := db.GetEngine(ctx).Table("pull_merge_queue_entry").
		Where("repo_id = ? AND base_branch = ?", repoID, baseBranch).
		Select("MAX(position) AS max_position").Get(&maxPosition)
	if err != nil {
		return 0, err
	}
	if !has {
		return 1, nil
	}
	return maxPosition + 1, nil
}

// EnqueueMergeQueueEntryOptions carries the per-PR merge details for a queue entry, mirroring what
// classic auto-merge stores on AutoMerge.
type EnqueueMergeQueueEntryOptions struct {
	RepoID                 int64
	BaseBranch             string
	PullID                 int64
	EnqueuedByID           int64
	HeadCommitID           string
	MergeStyle             repo_model.MergeStyle
	Message                string
	DeleteBranchAfterMerge bool
}

// EnqueueMergeQueueEntry adds a pull request to the merge queue for its base branch.
// It returns ErrAlreadyInMergeQueue if the pull request already has an active entry.
func EnqueueMergeQueueEntry(ctx context.Context, opts EnqueueMergeQueueEntryOptions) (*MergeQueueEntry, error) {
	if exists, _, err := GetActiveMergeQueueEntryByPullID(ctx, opts.PullID); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrAlreadyInMergeQueue{PullID: opts.PullID}
	}

	position, err := NextPosition(ctx, opts.RepoID, opts.BaseBranch)
	if err != nil {
		return nil, err
	}

	entry := &MergeQueueEntry{
		RepoID:                 opts.RepoID,
		BaseBranch:             opts.BaseBranch,
		PullID:                 opts.PullID,
		Position:               position,
		Status:                 MergeQueueEntryStatusQueued,
		HeadCommitID:           opts.HeadCommitID,
		EnqueuedByID:           opts.EnqueuedByID,
		MergeStyle:             opts.MergeStyle,
		Message:                opts.Message,
		DeleteBranchAfterMerge: opts.DeleteBranchAfterMerge,
	}
	if _, err := db.GetEngine(ctx).Insert(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// GetActiveMergeQueueEntryByPullID returns the active (non-terminal) merge queue entry for a pull request, if any.
func GetActiveMergeQueueEntryByPullID(ctx context.Context, pullID int64) (bool, *MergeQueueEntry, error) {
	entry := &MergeQueueEntry{}
	has, err := db.GetEngine(ctx).
		Where("pull_id = ? AND status IN (?, ?)", pullID, MergeQueueEntryStatusQueued, MergeQueueEntryStatusTesting).
		Get(entry)
	if err != nil || !has {
		return false, nil, err
	}
	return true, entry, nil
}

// CountQueuedEntriesAhead returns how many entries are still queued (not yet testing) strictly ahead
// of the given position on a repo+branch's queue. Used to show a PR's position in the queue.
func CountQueuedEntriesAhead(ctx context.Context, repoID int64, baseBranch string, position int64) (int64, error) {
	return db.GetEngine(ctx).Table("pull_merge_queue_entry").
		Where("repo_id = ? AND base_branch = ? AND status = ? AND position < ?", repoID, baseBranch, MergeQueueEntryStatusQueued, position).
		Count()
}

// GetQueuedEntriesForBranch returns up to `limit` queued (not yet testing) entries for a repo+branch, oldest first.
func GetQueuedEntriesForBranch(ctx context.Context, repoID int64, baseBranch string, limit int) ([]*MergeQueueEntry, error) {
	entries := make([]*MergeQueueEntry, 0, limit)
	err := db.GetEngine(ctx).
		Where("repo_id = ? AND base_branch = ? AND status = ?", repoID, baseBranch, MergeQueueEntryStatusQueued).
		OrderBy("position ASC").
		Limit(limit).
		Find(&entries)
	return entries, err
}

// GetMergeQueueEntriesByBatchID returns all entries belonging to a batch, ordered by their queue position.
func GetMergeQueueEntriesByBatchID(ctx context.Context, batchID int64) ([]*MergeQueueEntry, error) {
	var entries []*MergeQueueEntry
	err := db.GetEngine(ctx).Where("batch_id = ?", batchID).OrderBy("position ASC").Find(&entries)
	return entries, err
}

// HasActiveBatch reports whether the given repo+branch has a batch that has not reached a terminal state.
func HasActiveBatch(ctx context.Context, repoID int64, baseBranch string) (bool, error) {
	return db.GetEngine(ctx).Table("pull_merge_queue_batch").
		Where("repo_id = ? AND base_branch = ? AND status IN (?, ?)", repoID, baseBranch, MergeQueueBatchStatusBuilding, MergeQueueBatchStatusTesting).
		Exist()
}

// CreateMergeQueueBatch creates a new batch and assigns the given entries to it, marking them Testing.
func CreateMergeQueueBatch(ctx context.Context, repoID int64, baseBranch, baseCommitID string, mergeStyle repo_model.MergeStyle, entries []*MergeQueueEntry) (*MergeQueueBatch, error) {
	return db.WithTx2(ctx, func(ctx context.Context) (*MergeQueueBatch, error) {
		batch := &MergeQueueBatch{
			RepoID:       repoID,
			BaseBranch:   baseBranch,
			BaseCommitID: baseCommitID,
			MergeStyle:   mergeStyle,
			Status:       MergeQueueBatchStatusBuilding,
		}
		if _, err := db.GetEngine(ctx).Insert(batch); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			entry.BatchID = batch.ID
			entry.Status = MergeQueueEntryStatusTesting
			if _, err := db.GetEngine(ctx).ID(entry.ID).Cols("batch_id", "status").Update(entry); err != nil {
				return nil, err
			}
		}
		return batch, nil
	})
}

// UpdateMergeQueueBatch persists changed columns of a batch.
func UpdateMergeQueueBatch(ctx context.Context, batch *MergeQueueBatch, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(batch.ID).Cols(cols...).Update(batch)
	return err
}

// GetMergeQueueBatchByTempCommitID finds an in-flight batch by the SHA of its synthetic combined commit.
func GetMergeQueueBatchByTempCommitID(ctx context.Context, repoID int64, sha string) (bool, *MergeQueueBatch, error) {
	batch := &MergeQueueBatch{}
	has, err := db.GetEngine(ctx).
		Where("repo_id = ? AND temp_commit_id = ? AND status IN (?, ?)", repoID, sha, MergeQueueBatchStatusBuilding, MergeQueueBatchStatusTesting).
		Get(batch)
	if err != nil || !has {
		return false, nil, err
	}
	return true, batch, nil
}

// MarkMergeQueueEntryStatus updates the status (and optional removal reason) of a single entry.
func MarkMergeQueueEntryStatus(ctx context.Context, entryID int64, status MergeQueueEntryStatus, removalReason string) error {
	_, err := db.GetEngine(ctx).ID(entryID).Cols("status", "removal_reason").Update(&MergeQueueEntry{
		Status:        status,
		RemovalReason: removalReason,
	})
	return err
}

// RemoveMergeQueueEntryByPullID marks a pull request's active merge queue entry as removed, if any.
// It returns false if there was no active entry to remove.
func RemoveMergeQueueEntryByPullID(ctx context.Context, pullID int64, reason string) (bool, error) {
	exists, entry, err := GetActiveMergeQueueEntryByPullID(ctx, pullID)
	if err != nil || !exists {
		return false, err
	}
	return true, MarkMergeQueueEntryStatus(ctx, entry.ID, MergeQueueEntryStatusRemoved, reason)
}

// GetMergeQueueEntriesForRuleView returns entries whose base branch matches `match`, for display on
// a branch-protection rule's queue page: every non-terminal entry, plus the most recent `recentLimit`
// terminal ones. `match` should be the rule's Match method so glob rules (e.g. release/*) include
// every queued PR whose actual base branch is covered.
func GetMergeQueueEntriesForRuleView(ctx context.Context, repoID int64, match func(baseBranch string) bool, recentLimit int) ([]*MergeQueueEntry, error) {
	var branches []string
	if err := db.GetEngine(ctx).Table("pull_merge_queue_entry").
		Where("repo_id = ?", repoID).
		Distinct("base_branch").
		Cols("base_branch").
		Find(&branches); err != nil {
		return nil, err
	}
	matched := make([]string, 0, len(branches))
	for _, b := range branches {
		if match != nil && match(b) {
			matched = append(matched, b)
		}
	}
	if len(matched) == 0 {
		return nil, nil
	}

	var active []*MergeQueueEntry
	if err := db.GetEngine(ctx).
		Where("repo_id = ? AND status IN (?, ?)", repoID, MergeQueueEntryStatusQueued, MergeQueueEntryStatusTesting).
		In("base_branch", matched).
		OrderBy("position ASC").
		Find(&active); err != nil {
		return nil, err
	}

	if recentLimit <= 0 {
		return active, nil
	}

	var recent []*MergeQueueEntry
	if err := db.GetEngine(ctx).
		Where("repo_id = ? AND status IN (?, ?, ?)", repoID,
			MergeQueueEntryStatusSucceeded, MergeQueueEntryStatusFailed, MergeQueueEntryStatusRemoved).
		In("base_branch", matched).
		OrderBy("updated_unix DESC").
		Limit(recentLimit).
		Find(&recent); err != nil {
		return nil, err
	}

	return append(active, recent...), nil
}

// GetMergeQueueEntryByRepoAndID returns a single entry by ID only if it belongs to repoID.
func GetMergeQueueEntryByRepoAndID(ctx context.Context, repoID, id int64) (bool, *MergeQueueEntry, error) {
	entry := &MergeQueueEntry{}
	has, err := db.GetEngine(ctx).Where("id = ? AND repo_id = ?", id, repoID).Get(entry)
	if err != nil || !has {
		return false, nil, err
	}
	return true, entry, nil
}

// GetMergeQueueBatchByID returns a batch by its ID.
func GetMergeQueueBatchByID(ctx context.Context, id int64) (bool, *MergeQueueBatch, error) {
	batch := &MergeQueueBatch{}
	has, err := db.GetEngine(ctx).ID(id).Get(batch)
	if err != nil || !has {
		return false, nil, err
	}
	return true, batch, nil
}

// RequeueMergeQueueEntries resets a set of entries back to Queued (e.g. after a base-tip-drift abort),
// clearing their batch assignment so they are reconsidered by the worker.
func RequeueMergeQueueEntries(ctx context.Context, entryIDs []int64) error {
	if len(entryIDs) == 0 {
		return nil
	}
	_, err := db.GetEngine(ctx).In("id", entryIDs).
		In("status", MergeQueueEntryStatusQueued, MergeQueueEntryStatusTesting).
		Cols("batch_id", "status").Update(&MergeQueueEntry{
		BatchID: 0,
		Status:  MergeQueueEntryStatusQueued,
	})
	return err
}
