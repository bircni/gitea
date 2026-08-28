# Merge Queue for Gitea

## Implementation status

The full design is implemented, including batching, bisection, the dedicated Actions trigger, a
queue management page, and the GitHub-parity restrictions called out in the initial gap review:

- Data model, migration, and the `ProtectedBranch` settings: `EnableMergeQueue`,
  `MergeQueueMinBatchSize`/`MergeQueueMaxBatchSize` (GitHub calls these "minimum"/"maximum group
  size"), `MergeQueueWaitMinutes`, `MergeQueueMergeStyle` (§1).
- `MergeCheckTypeQueue` and the branch-protection skip logic (§5).
- **Direct merge is blocked** on a queue-enabled branch: `CheckPullMergeable` rejects
  `MergeCheckTypeGeneral` with `ErrMustUseMergeQueue` unless the doer force-merges with bypass
  permission, matching GitHub's "the merge button only offers Merge when ready" behavior. The PR
  page's `canMergeNow` computation was updated to match, so the direct-merge button doesn't even
  render for a non-bypassing doer.
- `services/pull.BuildMergeQueueBatchPreviewCommit`, supporting merge/squash/rebase/rebase-merge for
  batches of any size — rebase-style batches sequentially replay each PR's commits on top of the
  previous PR's replayed result, not a pairwise merge (§3).
- `services/mergequeue`: enqueue/dequeue (with per-PR custom merge title/message and delete-branch
  choice, mirroring classic auto-merge), a per-branch worker respecting `MergeQueueMinBatchSize`/
  `MergeQueueMaxBatchSize`/`MergeQueueWaitMinutes` (starts immediately once the minimum is queued or
  the wait timer expires, caps a batch at the maximum), the commit-status notifier that finalizes or
  bisects a batch, finalize-time base-tip-drift detection, and eviction on force-push/close/retarget
  via notifier hooks (§2, §4, §6).
- Enabling the queue requires at least one required status check (own rule or a required scoped
  workflow) to already be configured — otherwise there would be nothing for a batch to wait on.
- Bisection on failure: a failed batch of N>1 splits in half and each half is rebuilt from the
  original base tip (never reusing the failed batch's intermediate commits), recursing to size 1 (§4.5).
- Auto-merge repurposing: "merge when checks succeed" adds a PR to the queue instead of scheduling
  classic auto-merge on queue-enabled branches, in both the web and API routers; the cancel/remove
  endpoints on both handle whichever mechanism is actually active (§2).
- Web UI: a "Merge Queue" section in branch protection settings, a "queue enabled" badge + "View
  queue" link in the rule list, merge-box wording/status-tip changes on the PR page (position in
  queue / "currently being tested", cancel button), and a per-branch **queue management page**
  (`/{owner}/{repo}/settings/branches/queue?rule_id=...`) listing active and recent entries with an
  admin remove action (§7).
- The `on: merge_group` Actions trigger: a new `HookEventMergeGroup`/`GithubEventMergeGroup`, workflow
  `on:` matching **including `branches`/`branches-ignore` activity filters** against the batch's
  target branch, and an `api.MergeGroupPayload`; pushes to a merge queue's synthetic ref fire
  `merge_group` instead of `push`, so `on: push` workflows (filtered or not) never see the synthetic
  commit, and only workflows that explicitly declare `on: merge_group` run against it (§4, §8 step 2).

Known, deliberate simplifications (not gaps found in review, but worth naming):

- Bisection always splits a failing batch exactly in half rather than doing smarter root-cause
  isolation (e.g. skipping known-good sub-ranges) - GitHub's own algorithm isn't public, and a plain
  binary split is the standard, well-understood approach (the same idea as `git bisect`).
- No dedicated queue view *across branches* - the management page is per branch-protection rule, not
  a single repo-wide queue dashboard; GitHub's queue view is also scoped to one target branch at a
  time, so this matches rather than falls short.
- No visual distinction on the PR's checks tab between a normal check run and one that ran against
  the merge queue's synthetic commit, and no queue ETA estimate - purely cosmetic, low value relative
  to effort.

## Context

GitHub's [merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue) lets maintainers require that PRs merge only after being tested against an up-to-date, combined version of the target branch, processed in FIFO order (optionally batched). Gitea currently only has "auto-merge" (`services/automerge`): a PR is merged as soon as *its own* head commit's checks pass — there's no queue, no re-testing against a moving base tip, and no batching. This plan is a **full, GitHub-parity merge queue**: PRs opt into a per-branch queue, Gitea builds a temporary combined commit (batch) on top of the current base tip, runs required checks against that combined commit, and only then fast-forwards the results into the real base branch — refusing to leave "up to date with base" as a manual chore for contributors.

Rebase is supported as a merge queue strategy alongside merge-commit/squash — not just as an afterthought, since batching interacts differently with rebase (sequential replay) than with merge commits (pairwise merge), which affects both the batch-testing correctness and the bisection algorithm on failure.

This document is a design/plan deliverable — no code has been written as part of producing it.

## Chosen design (GitHub-parity, full batch queue)

- New opt-in, per-protected-branch feature: FIFO queue with configurable batch size and wait timer.
- Two new tables: `MergeQueueEntry` (one row per queued PR) and `MergeQueueBatch` (groups 1..N entries being tested together).
- Batch testing uses a **synthetic ref** `refs/for-merge-queue/<branch>/<batchID>` in the base repo (not a visible branch), built by reusing/extending the existing temp-clone merge machinery, and pushed so Gitea Actions' normal push-trigger fires required checks against it.
- **Rebase support is first-class**: for `MergeStyleRebase`/`MergeStyleRebaseMerge` batches, each queued PR's commits are sequentially rebase-replayed on top of the previous PR's replayed result (chained `doMergeStyleRebase` calls), so the tested tree is exactly what the real sequential rebase-merge would produce — not a synthetic merge commit that could pass while the real rebase result fails.
- On required-check failure, the batch bisects (binary split + rebuild-from-base-tip) to find the culprit PR and evict it, retesting the rest. Default batch size is **1 for all merge styles** (not just rebase), making batching an explicit opt-in power feature.
- The queue composes with existing merge machinery: final merges are done by calling `services/pull/merge.go`'s existing `Merge()` once per PR in order (not by force-pushing the precomputed synthetic commit), preserving all existing side effects (issue closing, webhooks, signing, comments) for free. At the default batch size of 1, this is simply one `Merge()` call per queue cycle, so the "redo git work" cost of replay vs. a direct fast-forward is negligible — reuse wins on both safety and cost.
- **Matches GitHub's actual UX**: GitHub doesn't run two competing mechanisms. On a queue-enabled branch, the PR merge button becomes "Merge when ready" — enabling "auto-merge" on such a PR enqueues it rather than merging it directly. This plan follows that: the existing auto-merge entry points (`pull.ScheduleAutoMerge` call sites) are repurposed to mean "enqueue" when the target branch has the queue enabled, rather than adding a second, independently-scheduled merge path that could race with the queue.

## 1. Data model

**`models/pull/merge_queue.go`** (new file, modeled directly on `models/pull/automerge.go`):

- `MergeQueueEntry` struct/table `pull_merge_queue_entry`: `RepoID`, `BaseBranch`, `PullID` (one active entry per PR), `Position` (FIFO order), `BatchID` (0 until picked up), `Status` (`Queued`/`Testing`/`Succeeded`/`Failed`/`Removed`), `HeadCommitID` (pinned at enqueue), `EnqueuedByID`, `MergeStyle`, `RemovalReason`, timestamps.
- `MergeQueueBatch` struct/table `pull_merge_queue_batch`: `RepoID`, `BaseBranch`, `BaseCommitID` (base tip batch was built on), `TempCommitID`, `TempRefName`, `MergeStyle`, `Status` (`Building`/`Testing`/`Succeeded`/`Failed`/`Bisecting`), timestamps.
- CRUD mirroring `ScheduleAutoMerge`/`GetScheduledMergeByPullID`/`DeleteScheduledAutoMerge`: `EnqueueMergeQueueEntry`, `GetMergeQueueEntryByPullID`, `GetQueuedEntriesForBranch` (ordered by `Position`), `RemoveMergeQueueEntry`, `NextPosition`.
- Enforce "one active entry per PR" via a select-then-insert-in-transaction check (follow whatever idiom `ScheduleAutoMerge` already uses for its upsert), not a raw unique index, for cross-DB portability.

**`models/git/protected_branch.go`** additions (same file/struct pattern as `EnableStatusCheck`, `StatusCheckContexts`, `RequiredApprovals`):

```go
EnableMergeQueue      bool
MergeQueueBatchSize   int    // default 1
MergeQueueWaitMinutes int    // default 5
MergeQueueMergeStyle  string // empty = repo default merge style
```

**Migration**: new file under `models/migrations/v1_XX/` (check `models/migrations/migrations.go` for the next version number), creating both tables and adding the four `protected_branch` columns, following an existing migration that does both `AddTableWithIndices` and `AddColumns`.

## 2. Enqueue path and relation to auto-merge

- `services/pull/check.go`'s `MergeCheckType` enum (currently `General`/`Manually`/`Auto`, `services/pull/check.go:126`) gets a new `MergeCheckTypeQueue` value.
- New package `services/mergequeue` (sibling to `services/automerge`, avoiding import cycles the same way `services/automergequeue` was split out) exposes `EnqueuePullRequest(ctx, pr, doer, mergeStyle)`:
  1. `CheckPullMergeable(ctx, doer, perm, pr, MergeCheckTypeQueue, mergeStyle, false)` — validates everything except "is it up to date with base" and "does its own head pass required checks" (see §5 for the exact skip list).
  2. `pull_model.EnqueueMergeQueueEntry(...)`.
  3. Posts a new comment (`CommentTypePRAddedToMergeQueue`, see §7.4) and notifies (§8).
  4. Pushes a `"<repoID>_<baseBranch>"` trigger onto the merge-queue processing queue (§3) to wake a worker.
- **Decision (matches GitHub)**: when `EnableMergeQueue` is true for a PR's base branch, the existing "auto-merge" scheduling entry points (`pull.ScheduleAutoMerge` call sites in `routers/web/repo/pull.go` and `routers/api/v1/repo/pull.go`) are repurposed to call `EnqueuePullRequest` instead — same button/API, different underlying action on queue-enabled branches, exactly like GitHub's "Merge when ready" behavior. There is exactly one consumer serializing merges into the branch. Flag the UI copy change ("Auto-merge" → "Merge when ready" on queue-enabled branches) explicitly in the PR description.

## 3. Batch building — including rebase support

New package `services/mergequeue`, file `batch_build.go`:

```go
func buildBatchTree(ctx context.Context, repo *repo_model.Repository, baseBranch string,
    entries []*pull_model.MergeQueueEntry, prs []*issues_model.PullRequest,
    mergeStyle repo_model.MergeStyle, doer *user_model.User) (finalCommitID string, err error)
```

- Uses a temp clone of the base repo at the current tip, generalizing `services/pull/merge_prepare.go`'s `createTemporaryRepoForMerge`/`mergeContext` machinery (extract a shared `prepareTempCloneForBase` helper since that function is currently PR-specific).
- **Merge/squash styles**: iterate PRs in `Position` order, merging each on top of the accumulating result (reuse `runMergeCommand` and the plumbing from `merge_merge.go`/`merge_squash.go`).
- **Rebase/rebase-merge styles**: do **not** merge each PR's head — sequentially rebase-replay each PR's commits on top of the *previous PR's replayed result*, reusing `rebaseTrackingOnToBase`/`doMergeStyleRebase` from `services/pull/merge_rebase.go` (confirmed present: `doMergeRebaseFastForward` for `MergeStyleRebase`, `doMergeRebaseMergeCommit` for `MergeStyleRebaseMerge`, both driven by `doMergeStyleRebase(ctx, mergeStyle, message)`). Concretely: PR1 rebases onto `baseTipSHA`; the temp base pointer advances to PR1's replayed result; PR2 rebases onto *that*, not the original tip; repeat. This makes the tested tree bit-for-bit what the real sequential rebase-merge will produce — the reason a synthetic merge-commit test would be insufficient for rebase-style queues.
- Both rebase variants (`MergeStyleRebase` and `MergeStyleRebaseMerge`) are supported from day one — `doMergeStyleRebase` already takes the style as a parameter, so chaining it per PR costs nothing extra; excluding rebase-merge would require additional special-casing for no savings.
- Push the resulting commit to `refs/for-merge-queue/<baseBranch>/<batchID>`; record `TempCommitID`/`BaseCommitID` on the `MergeQueueBatch` row.

**Why a synthetic ref, not a visible branch**: avoids branch-list/dropdown/API clutter, matches the existing `refs/pull/*` namespace precedent, and is cleanly excludable from normal branch-protection/branch-deletion logic.

## 4. CI triggering, queue worker, finalize, bisection

- **Triggering checks**: push the synthetic ref as a real ref in the base repo. Since most real-world workflows filter with `on: push: branches: [...]`, which won't match `refs/for-merge-queue/*`, **v1 includes a dedicated `on: merge_group` Actions trigger** (mirroring GitHub's own event name) rather than deferring it — relying on unfiltered `push` triggers would make the queue unusable for typical CI setups. This means the initial landing must also touch the Actions trigger-parsing plumbing: `on:` keyword parsing (search `models/actions` for where `push`/`pull_request` trigger names are matched — likely `models/actions/workflow*.go` or wherever `jobparser`/workflow-file trigger matching lives), the event-dispatch path that fires on push (`services/actions` push-notifier, e.g. `services/actions/notifier_helper.go`/`notifier.go`), and the event payload shape delivered to a `merge_group`-triggered run (GitHub's payload includes the merge group's head SHA and base SHA — the Gitea equivalent should expose `MergeQueueBatch.TempCommitID`/`BaseCommitID`). This is a nontrivial addition to Actions and is the main scope-increasing item in this plan — see §8 sequencing. Commit statuses are keyed by SHA (`models/git/commit_status.go`), not ref, so no schema change is needed for status posting/reading regardless.
- **Mapping results back to a batch**: new `services/mergequeue/notify.go` notifier reacting to `CreateCommitStatus` for SHAs matching an in-flight `MergeQueueBatch.TempCommitID`, reusing `services/pull/commit_status.go`'s `IsPullCommitStatusPass`/`MergeRequiredContextsCommitStatus` logic generalized to operate on a plain SHA + required-contexts list rather than a PR.
- **Worker**: `services/mergequeue/mergequeue.go` `Init()` creates a unique queue `merge_queue_batch` via `queue.CreateUniqueQueue`, registered with `graceful.GetManager()` — same pattern as `services/automerge/automerge.go`. Handler, under a per-branch `globallock.LockAndDo` (new `mergeQueueLockKey(repoID, baseBranch)`, mirroring `getPullWorkingLockKey` in `services/pull/merge.go`): pops up to `MergeQueueBatchSize` oldest `Queued` entries (or fewer once `MergeQueueWaitMinutes` elapses), re-validates each is still current (`HeadCommitID` unchanged, PR open), builds the batch, pushes the ref, marks `Testing`. Progress after that is event-driven by the notifier above, not polling.
- **Finalize (success)**: `services/mergequeue/finalize.go` calls the existing `Merge()` (`services/pull/merge.go`) once per PR in `Position` order with `wasAutoMerged=true`, preserving all its existing side effects — chosen over hard-resetting the base ref to the precomputed synthetic commit: at the default batch size of 1, this is a single `Merge()` call, so the "redo git work" cost is negligible, while a fast-forward-to-synthetic-commit approach would require reimplementing or looping all of `Merge()`/`handleMergePostProcess`'s per-PR bookkeeping (issue closing, webhooks, signing, comments) for a saving that only matters at larger batch sizes. Detects base-tip drift (something merged outside the queue between test-pass and finalize) by comparing current tip to `batch.BaseCommitID`; on drift, re-queues (not evicts) remaining entries.
- **Bisection (failure)**: `services/mergequeue/bisect.go`. Batch of 1 → that PR is the culprit, evict with a comment. Batch of N>1 → split by `Position`, **rebuild each half from `baseTipSHA`** (never reuse partial commits from the failed batch), retest recursively. Rebase-specific note: because rebase batches replay commits *relative to the previous PR's result* rather than merging pairwise, a full-batch rebase failure is more likely to stem from inter-PR interaction at the replay boundary than an equivalent merge/squash batch — mitigated by the batch size defaulting to 1 for every merge style, so bisection is only exercised when an admin explicitly opts into batching.

## 5. Branch-protection interaction (`CheckPullMergeable`)

`services/pull/check.go:141` — `CheckPullMergeable(ctx, doer, perm, pr, mergeCheckType, mergeStyle, forceMerge)` gets a `MergeCheckTypeQueue` branch. At enqueue time (`CheckPullBranchProtections`, `services/pull/merge.go:595`):

- **Still enforced**: permission, WIP, dependencies, signing requirements, required approvals, rejected/official reviews, codeowner reviews, protected-files.
- **Skipped at enqueue time** (needs a new parameter on `CheckPullBranchProtections`, e.g. `skipStatusCheck`): `IsPullCommitStatusPass` against the PR's own head (the batch test against the combined commit supersedes this — matches GitHub, which only requires checks on the merge-group commit) and `MergeBlockedByOutdatedBranch` (the queue's synthetic rebuild onto current tip *is* the up-to-date guarantee).
- At finalize time, `Merge()` re-validates normally — no skip flags needed there since the PR has by then actually been tested against a current base.

## 6. Concurrency and races

- Per-branch serialization via `globallock`, as above — same primitive already used for per-PR merge serialization (`getPullWorkingLockKey`), just keyed per-branch instead of per-PR.
- **Base branch pushed to directly while a batch is in flight**: detected at finalize via tip comparison (§4); re-queues rather than evicts.
- **PR head force-pushed while queued**: hook into the existing PR-synchronize notifier path to remove the entry (`Status = Removed`) and comment, matching GitHub's behavior of dropping a PR from the queue on new pushes.
- **PR closed/retargeted while queued**: hook into existing close/retarget notifier paths the same way.
- **Double-enqueue**: guarded by the transactional "one active entry per PR" check in `EnqueueMergeQueueEntry` (§1).

## 7. API / UI

- **Settings**: `templates/repo/settings/protected_branch.tmpl` gets a "Merge Queue" section (enable checkbox, batch size, wait-timer minutes, merge-style select reusing the existing style-option partial from `templates/repo/settings/options.tmpl`). `routers/web/repo/setting/protected_branch.go`'s `SettingsProtectedBranchPost` and the `ProtectBranchForm` (`services/forms`) gain the four fields. `branches.tmpl` rule list gets a "queue enabled" badge.
- **PR page**: merge box gets an "Add to merge queue" button (replacing the auto-merge button on queue-enabled branches) and a queue-status widget (position when `Queued`; "testing as part of batch with !X, !Y" when `Testing`, linking to check results against `TempCommitID`) plus a "Remove from queue" action.
- **API** (`routers/api/v1/repo/pull.go` + route registration): `POST/DELETE/GET /repos/{owner}/{repo}/pulls/{index}/merge-queue`, permission-gated the same way as the existing `MergePullRequest` handler.
- **Comment types** (`models/issues/comment.go`, alongside `CommentTypePRScheduledToAutoMerge`): `CommentTypePRAddedToMergeQueue`, `CommentTypePRRemovedFromMergeQueue`, with rendering added wherever the auto-merge comment case is handled in `templates/repo/issue/view_content/comments.tmpl`.
- **Notifications** (`services/notify/notifier.go`): `PullRequestAddedToMergeQueue`/`PullRequestRemovedFromMergeQueue`, implemented by the webhook notifier (new `action` values on the PR webhook payload) — mail can piggyback on the existing comment-triggered mail path.

## 8. Suggested landing sequence

1. Data model + migration (no behavior change, `EnableMergeQueue` defaults false).
2. `on: merge_group` Actions trigger keyword (trigger parsing, push-dispatch matching, payload shape) — pulled forward into the initial scope since v1 depends on it for required checks to actually run against the synthetic ref; this is the single biggest scope item in this plan, and is closer to an Actions-team review surface than a pull-service one, so consider landing it as its own preceding PR even though it's not deferred to "later."
3. `MergeCheckTypeQueue` + `services/mergequeue` skeleton + worker handling only batch size 1 (no real batching, no bisection) **including rebase support from the start** — this alone is "serialize PRs one at a time, each tested against a current base via a real `merge_group`-triggered check," already a strict improvement over today's automerge, and independently reviewable. Rebase is included here rather than deferred, since testing semantics would otherwise silently diverge from real rebase output.
4. Batch size > 1 + bisection, as a follow-up once (3) is stable — batching is opt-in and off by default, so it's safe to ship later without blocking the core queue.
5. UI/API surface — can land incrementally alongside (3)/(4).
6. Richer webhook/notifier events — follow-up.

## Key files to touch or model new code on

- `services/pull/merge_rebase.go` — `doMergeStyleRebase`, `rebaseTrackingOnToBase`, `doMergeRebaseFastForward`, `doMergeRebaseMergeCommit` (confirmed signatures) — reused/chained per PR for rebase batch building.
- `services/pull/merge_prepare.go` — `mergeContext`/`createTemporaryRepoForMerge` — generalize for multi-PR batch building.
- `services/pull/check.go` — `MergeCheckType` enum (line 126), `CheckPullMergeable` (line 141, confirmed signature includes `mergeStyle`).
- `services/pull/merge.go` — `Merge()`, `getPullWorkingLockKey`, `CheckPullBranchProtections` (line 595, confirmed signature).
- `services/automerge/automerge.go`, `services/automergequeue/automergequeue.go` — queue-registration and import-cycle-avoidance pattern to mirror in `services/mergequeue`.
- `models/git/protected_branch.go` — `ProtectedBranch` struct (line 30) for new fields; existing checks (`HasEnoughApprovals`, `MergeBlockedByOutdatedBranch`, etc.) partially reused, partially bypassed.
- `models/pull/automerge.go` — direct template for `models/pull/merge_queue.go`.

## Verification

Since this is plan/design only, "verification" here means how the eventual implementation should be validated:

- Unit tests: `models/pull/merge_queue_test.go` (CRUD/ordering), `services/pull/check_test.go` (extend for `MergeCheckTypeQueue` skip behavior), `services/mergequeue/batch_build_test.go` (merge/squash/rebase/rebase-merge batch construction against temp git fixtures — the rebase case specifically asserting the batch result matches sequential single-PR `doMergeStyleRebase` output), `services/mergequeue/bisect_test.go` (split-and-rebuild-from-base-tip correctness).
- Integration tests under `tests/integration/`, following `pull_merge_test.go`/`repo_branch_protection_test.go` conventions: end-to-end enqueue → synthetic check pass → FIFO merge; failure/bisection eviction; force-push-while-queued removal; direct-push-to-base-while-testing requeue.
- Run via `go test -run '^TestName$' ./services/mergequeue/...` etc. per repo convention, plus the standard integration-test invocation (`make test-sqlite` or equivalent).

## Decisions confirmed for this plan

1. **Auto-merge relationship**: auto-merge's existing entry points are repurposed to mean "enqueue" on queue-enabled branches (matches GitHub's actual "Merge when ready" behavior) — not a second competing merge path.
2. **Actions trigger**: `on: merge_group` is in scope for v1, not deferred — required checks must actually run against the synthetic ref out of the box. This is the largest scope addition versus a minimal-v1 approach; recommend sequencing it as its own preceding PR (§8).
3. **Finalize strategy**: replay real per-PR merges via the existing `Merge()`, sequentially — reuses all existing side-effect logic and costs nothing extra at the default batch size of 1.
4. **Default batch size**: 1, for every merge style (not just rebase) — batching above 1 is an explicit, off-by-default opt-in.
