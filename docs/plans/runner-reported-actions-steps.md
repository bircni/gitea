# Runner-reported Actions steps (issue #24604)

## Context

Gitea Actions currently determines a job's step list by **statically parsing the workflow YAML on the Gitea side**, before the job runs. The runner then parses the *same* YAML and the two sides correlate steps purely by **positional index**. This is the root cause of several long-standing bugs (#24604, #26736, #36983):

- An action's **Pre/Post** stages inherit their parent step's number, so cleanup/setup work isn't shown as its own step.
- **Service-container** init/stop and **job-container** lifecycle carry no step number → folded into job-level logs.
- **Reusable workflows** (`workflow_call`) run steps that don't exist in this job's parsed `steps:` at all.
- All of the above get swallowed into the synthetic **"Set up job" / "Complete job"** entries that Gitea fabricates in `FullSteps`.

The fix the issue asks for: **let the runner — which actually executes the steps — report them (with names) dynamically**, instead of Gitea guessing from the file. The runner is the only component that knows the real, complete step list.

**Hard constraint (from the user):** any old↔new combination of Gitea and runner must keep working. We achieve this with the **capability negotiation that already exists** in the codebase (precedent: the `"cancelling"` capability), so the new behavior only activates when *both* sides advertise support; otherwise both fall back to today's positional scheme.

Repos involved (all siblings under `/Users/nicolas/Github/go-gitea/`):
- `actions-proto-def` — proto source of truth
- `actions-proto-go` — generated Go bindings (published module `gitea.dev/actions-proto-go`, pinned at **v0.6.0** in both Go repos)
- `runner` — act_runner
- `gitea` — server

---

## Design overview

Add step identity (name + stable number + stage) to the `StepState` proto message. Gate the whole behavior behind a new capability string, e.g. **`reporting_steps`**, negotiated both directions. When negotiated:

- The **runner** reports the full, real step list it executes (including pre/post/service/setup), each `StepState` carrying a `name`, a stable `number`, and a `stage`.
- **Gitea** stops pre-creating step rows from its YAML parse and instead **upserts `ActionTaskStep` rows from the runner's report**, and stops fabricating "Set up job"/"Complete job" in `FullSteps` because the runner now reports them as real steps.

When *not* negotiated, every code path keeps today's exact behavior.

### Backward-compatibility matrix
| Runner | Gitea | Behavior |
|--------|-------|----------|
| old | old | positional steps (status quo) |
| **new** | old | Gitea doesn't advertise `reporting_steps` → runner falls back to `ResetSteps(len(job.Steps))` + positional; new proto fields ignored by old Gitea |
| old | **new** | runner doesn't advertise `reporting_steps` → Gitea pre-parses + positional upsert by index, `name` empty → keeps DB name |
| **new** | **new** | dynamic runner-reported steps (the fix) |

The new proto fields are additive (proto3, new field numbers), so even a version skew on one side only degrades gracefully.

---

## Phase 1 — Proto (`actions-proto-def` + `actions-proto-go`)

**File:** `actions-proto-def/proto/runner/v1/messages.proto`, message `StepState` (currently fields 1–6).

Add:
```proto
message StepState {
  int64 id = 1;
  Result result = 2;
  google.protobuf.Timestamp started_at = 3;
  google.protobuf.Timestamp stopped_at = 4;
  int64 log_index = 5;
  int64 log_length = 6;
  string name = 7;    // Display name of the step as the runner actually executed it.
  int64 number = 8;   // Stable step number assigned by the runner; identity key when reporting_steps is negotiated.
  string stage = 9;   // "Pre" | "Main" | "Post" (act stepStage), for sub-step grouping/labeling.
}
```
- Keep `id` as the wire/array index for old servers. `number` is the stable identity for the new path (so reordered/late-discovered steps don't collide).
- Regenerate bindings (`make` in `actions-proto-def`, using `buf` per its Makefile), publish/tag **actions-proto-go v0.7.0**.
- Bump `gitea.dev/actions-proto-go` to v0.7.0 in **both** `gitea/go.mod` and `runner/go.mod`; run `make tidy` in each.

No new capability field is needed in the proto — `RegisterRequest`/`DeclareRequest` already have `repeated string capabilities` (added in proto #17).

---

## Phase 2 — Capability negotiation (both repos, follow the `cancelling` precedent)

**Gitea → runner** (already via `X-Gitea-Actions-Capabilities` header):
- `gitea/models/actions/run_job_summary.go:32` `RunnerCapabilities()` — add the new constant to the comma-separated list (today it returns only `JobSummaryCapability`).
- Define `const reportingStepsCapability = "reporting_steps"` alongside `JobSummaryCapability`.

**Runner → Gitea** (via proto `capabilities` in Register/Declare):
- `runner/internal/app/run/runner.go:44-51` — add `const CapabilityReportingSteps = "reporting_steps"` and include it in `RunnerCapabilities()`.
- `gitea/routers/api/actions/runner/runner.go:117` — add a sibling to `runnerCapabilityCancelling`; in `applyDeclareRequestToRunner` (line 126) set a new `runner.HasStepReportingSupport` column (mirror `HasCancellingSupport`, incl. the `cols` append and a migration adding `has_step_reporting_support` to `ActionRunner`, model at `gitea/models/actions/runner.go:68`).

**Runner consumes Gitea's capability:** `r.capabilities` is already populated from the response header (`SetCapabilitiesFromDeclare`, runner.go:241-246) and forwarded as `GITEA_ACTIONS_CAPABILITIES`. Thread a parsed `reportingSteps bool` into `report.NewReporter(...)` (reporter.go:76) so the reporter knows which scheme to use.

---

## Phase 3 — Runner: report real steps (`runner`)

The reporter today pre-sizes `r.state.Steps` to `len(job.Steps)` and looks up steps by `entry.Data["stepNumber"]`. act already tags every log entry with `stepNumber`, `step` (name), `stepID`, and `stage` (`Pre`/`Main`/`Post`) via `withStepLogger` (`runner/act/runner/logger.go:158`).

When `reportingSteps` is negotiated:
- **`runner/internal/pkg/report/reporter.go`**
  - `NewReporter` (line 76): store the `reportingSteps` flag.
  - `ResetSteps` (line 122): in the new mode, do **not** pre-fill a fixed slice; allocate lazily.
  - `Fire` (line 181-273): when a log entry arrives with `stepNumber`/`step`/`stage`, **find-or-create** the `StepState` keyed by the act step number, populating `Name`, `Number`, and `Stage` on first sight. This naturally captures pre/post stages and any step act runs that Gitea never parsed. Keep populating `Result`, `LogIndex`, `LogLength`, timestamps exactly as now.
  - Service-container / job-container lifecycle currently log without `stepNumber` (job-level → "Set up job"/"Complete job"). Decide per-task: minimally, keep them job-level (matches GitHub's implicit setup), since the win is correct *declared/reusable/pre/post* steps. Emitting them as explicit steps would additionally require act to tag those lifecycle phases with synthetic step numbers (`run_context.go` `startContainer`/`startServiceContainers`) — list as an optional follow-up, not required for the core fix.
- Old mode: unchanged (`ResetSteps(len(job.Steps))`, positional), set in `runner/internal/app/run/runner.go:330`.

---

## Phase 4 — Gitea: persist runner-reported steps (`gitea`)

- **`gitea/models/actions/task.go`**
  - `claimJobForRunner` (lines 324-339): when the runner `HasStepReportingSupport`, **skip** pre-inserting `ActionTaskStep` rows (or insert them but mark them provisional). The runner will report the authoritative list. Keep the current pre-parse path for non-supporting runners.
  - `UpdateTaskByState` (lines 419-495): today it only updates rows already in `task.Steps`, matched by `step.Index == StepState.Id`, dropping any unmatched reported step. In the new mode, **upsert**: for each reported `StepState`, find the row by `number` (fallback `id`); if absent, `Insert` a new `ActionTaskStep` (Name from `StepState.Name` via the same truncation as `makeTaskStepDisplayName`/`EllipsisDisplayString`, Index/Number from the report, TaskID/RepoID from the task), else update. Preserve the existing positional update path when the runner doesn't support reporting.
- **`gitea/modules/actions/task_state.go`** `FullSteps` (lines 15-123): when steps were runner-reported (e.g. the task/job carries the supporting flag, or the reported steps already include setup/complete), **do not synthesize** "Set up job"/"Complete job" — return the real steps as-is. Keep the synthetic injection for the legacy path. This is where the user-visible bug actually resolves.
- **`gitea/models/actions/task_step.go`**: add a `Number int64` column if `number` is to be the persisted identity key (migration), or reuse `Index` and treat the runner's `number` as `Index`. Prefer reusing `Index` to avoid schema churn unless gaps/reordering require a distinct stable key.

---

## Critical files (by repo)

- **actions-proto-def:** `proto/runner/v1/messages.proto` (StepState)
- **actions-proto-go:** regenerate + tag v0.7.0
- **runner:** `internal/pkg/report/reporter.go` (ResetSteps/Fire/NewReporter), `internal/app/run/runner.go` (RunnerCapabilities, reporter wiring), `go.mod`
- **gitea:** `models/actions/task.go` (claimJobForRunner, UpdateTaskByState), `models/actions/task_step.go`, `models/actions/runner.go` (+ migration), `models/actions/run_job_summary.go` (RunnerCapabilities), `routers/api/actions/runner/runner.go` (applyDeclareRequestToRunner), `modules/actions/task_state.go` (FullSteps), `go.mod`

---

## Verification

1. **Unit (runner):** extend `internal/pkg/report/reporter_test.go` — in `reporting_steps` mode, feed `Fire` entries with new `stepNumber`s/names not pre-allocated and assert `StepState`s are created with correct `Name`/`Number`/`Stage`/results. Existing positional tests must still pass unchanged (old mode).
2. **Unit (gitea):** extend `models/actions/task_test.go` — `UpdateTaskByState` with a `TaskState` whose `Steps` include names + numbers not pre-created → assert new `ActionTaskStep` rows are inserted; with a non-supporting runner → assert legacy positional behavior. Add a `FullSteps` test asserting no synthetic Set up/Complete job is injected when steps are runner-reported.
3. **Capability tests:** mirror `runner_test.go` assertions for the new `has_step_reporting_support` column and the header list from `RunnerCapabilities()`.
4. **Backward-compat smoke (manual / integration):** run all four matrix combinations against a workflow that uses a service container + an action with pre/post (e.g. `actions/checkout`) + a `workflow_call`. Confirm: new↔new shows the real extra steps; the three legacy combinations render identically to today.
5. `make lint-go` + `make test` in gitea; `make` test target in runner; `make tidy` in both after the go.mod bump.

## Open questions / sequencing notes

- Proto + actions-proto-go must land and tag **first**; the two Go repos depend on the published v0.7.0. Until then, develop against a `replace` directive pointing at the local `../actions-proto-go` checkout.
- Whether to surface service/job-container lifecycle as explicit steps (Phase 3 optional follow-up) needs maintainer input — it requires act-level changes and changes GitHub-parity expectations.
- Persisted identity: reuse `Index` vs. add `Number` column — decide during Phase 4 based on whether reusable-workflow steps need a non-contiguous key.
