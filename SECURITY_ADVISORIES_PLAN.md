# Repository Security Advisories for Gitea — Milestone 1

> **Status: not started.** No implementation code exists yet. This document is the approved
> implementation plan, written to be picked up by a fresh session with no prior context.
> Read `AGENTS.md` at the repo root first — it carries the mandatory contribution rules
> (`make fmt` after Go edits, Conventional Commits, `Assisted-by:` trailer, no `Co-Authored-By`).

## Environment facts — verified in this tree, and easy to get wrong

A fresh session will trip on all of these. They were each checked against the code, not assumed.

- **The Go module path is `gitea.dev`**, not `code.gitea.io/gitea`. Imports look like
  `gitea.dev/models/user`. (`go.mod:1`)
- **Migrations live in the top-level `modelmigration/`**, not `models/migrations/`. Current folder is
  `modelmigration/v28/`; highest migration is **348**, so the next is `v349.go`, registered in
  `modelmigration/migrations.go`.
- **Brand-new tables need no migration file.** `InitEngineWithMigration` runs `SyncAllTables()`
  *after* migrations (`models/db/engine_init.go:116`), so XORM creates any model registered with
  `db.RegisterModel`. A migration is only required to alter or backfill an *existing* table.
- **Locale is flat-key JSON**, not INI: `options/locale/locale_en-US.json`. Edit only `en-US`; the
  rest sync from Crowdin.
- **ActivityPub was deleted from this tree** in `1592576fa5`. `routers/api/v1/activitypub/person.go`
  is 14 lines returning 501. There is no federation to build on.
- **Do not add a new `unit.Type`.** See the locked decision below.

## Context

Gitea has **no** security-advisory surface at all. A repo-wide grep for advisory/CVE/GHSA/OSV/SBOM
returns zero product code. Gitea itself outsources its own vulnerability handling to GitHub
(`SECURITY.md` points at `github.com/go-gitea/gitea/security/advisories/new`), which is an awkward
dependency for a self-hosted forge and impossible for any air-gapped instance. The one prior
proposal, https://github.com/go-gitea/gitea/issues/27462, was closed unaccepted; Forgejo's
equivalent discussion (https://codeberg.org/forgejo/discussions/issues/468) was also closed.

The goal is a coordinated-disclosure workflow that is *better shaped* than GitHub's, not a clone.
GitHub's own community thread (https://github.com/orgs/community/discussions/189802) documents the
failure modes we should design out from day one:

- Report quality has collapsed — GitHub's words: reports are *"AI-generated with minimal or no human
  review"* and *"validating even a single poor-quality report can take hours."* Gitea's `SECURITY.md`
  already fights this with prose ("Do not use LLM to write the reports", "open an advisory per found
  issue", "verify it reproduces on a supported version"). **Those rules should be enforced by the
  intake form, not by asking nicely.**
- No cross-repo triage — *"I have to go to 70 different pages to triage reports."*
- Permissions are all-or-nothing — *"I couldn't add someone to a submission I made."*
- Version-range data is unreliable. `github/advisory-database` issues
  [#771](https://github.com/github/advisory-database/issues/771) and
  [#6686](https://github.com/github/advisory-database/issues/6686) show unbounded and
  contradicted ranges shipping to every downstream scanner.

**Outcome:** a repo can privately receive a structured vulnerability report, triage and draft an
advisory behind a per-advisory ACL, then publish it to a repo/org/instance-wide list with a
machine-readable OSV export and a webhook — entirely on-instance.

## Locked decisions

| Decision | Choice |
|---|---|
| Scope | Advisory core only (see *Out of scope*) |
| Access control | Per-advisory ACL. **No new `unit.Type`.** |
| Private fix collaboration | Not in this milestone |
| Field shape | GitHub-API-shaped, additively extended |

**Why no new unit:** `models/unit/unit.go:36` carries a maintainer FIXME — *"admin team won't inherit
the correct admin permission for the new unit, need to have a complete fix before adding any new
unit"* (corroborated at `routers/web/org/teams.go:330`, `models/organization/team_repo.go:55`). This
is also the *correct* model regardless: on GitHub, repo write does **not** grant draft-advisory
access. Advisory access is an explicit per-object collaborator list, which no unit can express.

**One concern to record, then proceed:** GitHub's `vulnerable_version_range` is free text, and that
is the direct cause of the false-positive problem cited above. We keep the field shape for API
compatibility but **validate and normalize it on save** (below), so the data is machine-usable when a
future dependency-alerts milestone consumes it. No behaviour is lost; the defect simply isn't
inherited.

## Data model

New package `models/security/`. All tables are new, so **no migration file is needed** —
`InitEngineWithMigration` runs `SyncAllTables()` after migrations (`models/db/engine_init.go:116`),
and XORM creates registered models. Register each with `db.RegisterModel(new(T))` in an `init()`.

```go
// security_advisory
type Advisory struct {
    ID          int64  `xorm:"pk autoincr"`
    RepoID      int64  `xorm:"INDEX NOT NULL"`
    GTSAID      string `xorm:"UNIQUE NOT NULL"`      // GTSA-xxxx-xxxx-xxxx
    CVEID       string `xorm:"INDEX"`
    State       State  `xorm:"INDEX NOT NULL"`       // triage|draft|published|closed|withdrawn
    Summary     string `xorm:"VARCHAR(1024) NOT NULL"`
    Description string `xorm:"LONGTEXT"`
    Severity    Severity                             // critical|high|medium|low
    CVSSv3Vector string
    CVSSv3Score  float64
    CVSSv4Vector string
    CVSSv4Score  float64
    CWEIDs      []string `xorm:"JSON TEXT"`
    AuthorID    int64    `xorm:"INDEX NOT NULL"`
    PublisherID int64
    CreatedUnix, UpdatedUnix timeutil.TimeStamp
    PublishedUnix, ClosedUnix, WithdrawnUnix timeutil.TimeStamp
}
```

Plus `AdvisoryVulnerability` (`AdvisoryID`, `Ecosystem`, `PackageName`, `VulnerableVersionRange`,
`PatchedVersions`, `VulnerableFunctions []string JSON`), `AdvisoryCredit`, `AdvisoryCollaborator`,
`AdvisoryComment`.

Three deliberate, additive deviations from GitHub — all permitted by `docs/guidelines-backend.md`
("If Gitea offers functionality GitHub does not… a new field may be added"):

1. **`Ecosystem` is a validated open string, not a closed enum.** GitHub refuses community
   contributions for C/C++ and anything without a registry
   ([advisory-database#2963](https://github.com/github/advisory-database/issues/2963)). We accept
   GitHub's 13 values *plus* OSV ecosystem strings, and allow `other` with a free-text package name.
2. **CVSS score is derived, never hand-entered.** Parse the vector and compute the score; reject a
   `Severity` that contradicts it. GitHub lets these drift apart. **Scope limit:** score v3.1
   natively (~150-line formula); for v4.0, validate and store the vector but do **not** derive a
   score. Full v4.0 scoring needs the MacroVector lookup table (~270 entries, 800–1,000 lines) or a
   new dependency, and `docs/guidelines-backend.md` requires any `go.mod` change be justified in the
   PR and verified against an upstream commit. Authors paste vectors from a calculator regardless.
3. **`VulnerableVersionRange` / `PatchedVersions` are parsed on save** by a new
   `modules/security/versionrange` (accepting GitHub's `>= 1.0.0, < 1.2.3` syntax), storing the
   normalized form. Unbounded ranges are a validation warning surfaced in the publish checklist.
   Note `hashicorp/go-version` (the only semver lib in tree) validates but cannot evaluate
   constraints — this module owns parsing only; evaluation is a later milestone's problem.

**Credits** follow the established non-user attribution convention — the
`(UserID, OriginalAuthor, OriginalAuthorID)` triple used by `models/issues/comment.go:260` and
rendered by `templates/repo/issue/view_content/comments_authorlink.tmpl`. `UserID > 0` for local
users, `UserID == 0` + `OriginalAuthor` for outside reporters. Credit `Type` reuses OSV's 10 roles
(finder/reporter/analyst/coordinator/remediation_developer/reviewer/verifier/tool/sponsor/other) and
`State` is `pending|accepted|declined`. Never render `u.Email`; call `u.GetEmail()`
(`models/user/user.go:221`) so `KeepEmailPrivate` is honoured.

**Comments** get their own `AdvisoryComment` table rather than reusing `models/issues.Comment`, whose
`IssueID` is non-nullable and whose every list query would need an exclusion filter. Isolation is the
right call for embargoed content. Rendering reuses
`models/renderhelper.NewRenderContextRepoComment` unchanged, so `#123` refs and `@mentions` work.

**Two columns on `repository`** (`models/repo/repo.go`), matching the existing
`NumProjects`/`NumOpenProjects` idiom, so nav gating costs zero extra queries:
`PrivateReportingEnabled bool` and `NumPublishedAdvisories int`. These *do* alter an existing table —
add `modelmigration/v28/v349.go` and register `newMigration(349, …)` in
`modelmigration/migrations.go` (current highest is 348). Follow the local-struct-redeclaration
convention of `modelmigration/v28/v345.go` and use `SyncWithOptions(IgnoreDrop…)`.

## Access control

A dedicated `services/security/perm.go` computes advisory access. It must **not** be derived from
`access_model.Permission`, because `GetIndividualUserRepoPermission` short-circuits to
`AccessModeOwner` for `user.IsAdmin || user.ID == repo.OwnerID` without consulting units, and an org
admin team sets `unitsMode = nil` entirely (`models/perm/access/repo_permission.go:480`).

```
AdvisoryAdmin  := repo owner, org owner, or repo Permission.IsAdmin()
AdvisoryWrite  := AdvisoryAdmin, or a row in security_advisory_collaborator
AdvisoryRead   := AdvisoryWrite, or advisory.AuthorID == doer.ID
Published      := anyone who can read the repo
```

*Assumption, easily reversed:* site admins retain access, consistent with how they already see every
private repo. Flipping this is one condition in one function.

**The critical implementation rule:** filter in SQL, never in Go after fetching. Add
`AccessibleAdvisoryCondition(doer) builder.Cond` in `models/security/advisory_list.go`, modelled on
`AccessibleRepositoryCondition` (`models/repo/repo_list.go:669`), and use it in *every* list, count,
and search path. Existence of an unpublished advisory must never leak — all failures are
`ctx.NotFound(nil)`, matching `services/context/permission.go`.

Cross-reference safety: if `GTSA-` refs become linkable, mirror the permission check in
`(issue *Issue) verifyReferencedIssue` (`models/issues/issue_xref.go:191`) so a public issue cannot
confirm an embargoed advisory exists.

## Lifecycle

```
                 ┌─ report ─→ triage ─┬─→ draft ─→ published ─→ withdrawn
maintainer ──────┴─→ draft ───────────┘     └─→ closed
```

`triage` only exists for reports arriving via private reporting. Accepting a report converts it to
`draft` (the reporter is auto-credited as `reporter`, pending their acceptance). `closed` is a
rejected/invalid report. `withdrawn` is post-publication retraction — kept visible with a banner,
never deleted, and reflected as OSV `withdrawn`.

## Intake — the quality gates

This is where the milestone earns "better than GitHub". Reuse `modules/issue/template` wholesale —
`Unmarshal`, `Validate`, `RenderToMarkdown` (`modules/issue/template/{template,unmarshal}.go`) —
which already provides typed `input`/`textarea`/`dropdown`/`checkboxes` fields with required-field
validation, loaded from git. Add `.gitea/SECURITY_REPORT.yaml` (with `.github/` fallback) to
`services/security/template.go`, following `services/issue/template.go:24`.

Ship a built-in default form used when the repo has no template, enforcing what Gitea's `SECURITY.md`
currently only requests in prose:

- **Affected version** (required) validated against the repo's releases — a report against an EOL
  version is rejected at submission with a link to the support policy.
- **Reproduction steps** (required, min length).
- **Impact** and **severity self-assessment** (required dropdown).
- **AI-assistance disclosure** (required checkbox set) — mirroring *"If you have used LLM to find the
  bug, be transparent about it and verify that it is not hallucination."*
- **Single-issue attestation** — mirroring *"open an advisory per found issue."*

Two more gates in `services/security/report.go`:

- **Duplicate detection at submit time**: search this repo's existing advisories by package +
  CWE + summary trigram before accepting, and show near-matches to the reporter. GitHub has only
  *announced* this.
- **Rate limiting per reporter per repo**, so one actor cannot flood a maintainer.

`ProhibitLogin`/blocked users cannot submit. Private reporting is off by default and enabled per repo.

## Publish-time checklist

Before `published` is allowed, `services/security/publish.go` validates and surfaces blocking errors
vs. warnings: summary present; at least one affected package; every version range parses; **a patched
version is set** (GitHub only documents this as advice, and its absence is what breaks downstream
consumers); CVSS vector parses and agrees with severity; every credit resolved or explicitly
anonymous. Publishing sets `PublishedUnix`, increments `Repository.NumPublishedAdvisories`, fires the
webhook, and emails credited parties + repo watchers.

## Backend layout

```
models/security/          advisory.go, advisory_list.go, vulnerability.go, credit.go,
                          collaborator.go, comment.go, gtsa_id.go
modules/security/         cvss/ (vector parse + score), versionrange/ (parse + normalize),
                          osv/ (export mapping)
services/security/        advisory.go, report.go, publish.go, perm.go, template.go, notify.go
routers/web/repo/security/  advisories.go, view.go, report.go
routers/api/v1/repo/      security_advisory.go
```

`GTSAID` generation lives in `models/security/gtsa_id.go`: `GTSA-` plus three 4-char groups from
GitHub's ambiguity-free alphabet `23456789cfghjmpqrvwx`, with a `UNIQUE` constraint and retry on
collision. No per-repo sequential index — the ID is the URL key, as on GitHub.

## Web UI

Routes in `routers/web/web.go`, following the `/projects` group pattern at line 1530 (including the
trailing `// end "..."` comment convention):

```
/{owner}/{repo}/security                          overview + advisory list
/{owner}/{repo}/security/advisories/new           maintainer-authored draft
/{owner}/{repo}/security/advisories/report        structured reporter form
/{owner}/{repo}/security/advisories/{gtsa_id}     detail + discussion + collaborators + credits
/{owner}/-/security                               org triage inbox  (fixes "70 different pages")
/-/advisories                                     instance-wide published list
```

Guards are handler-level (`MustEnableSecurityAdvisories`, mirroring
`routers/web/repo/projects.go:39`), not `RequireUnitReader`, since there is no unit.

Nav tab in `templates/repo/header.tmpl`, gated on a `ctx.Data` boolean set from the two new
`repository` columns — precedent at line 171 (`{{if .Permission.IsAdmin}}` for Settings) and line 117
(`{{if and .EnableActions …}}`). Show when the viewer is a repo admin, or the repo has published
advisories, or private reporting is enabled for a signed-in viewer. Icon `octicon-shield`.

Templates under `templates/repo/security/`; frontend `web_src/js/features/repo-security.ts`
registered in `web_src/js/index.ts`. Per `AGENTS.md`, prefer `tw-*` utilities over inline styles.

Repo settings: a "Private vulnerability reporting" checkbox in
`templates/repo/settings/options.tmpl`, field on `RepoSettingForm`
(`services/forms/repo_form.go`), handled in `routers/web/repo/setting/setting.go` — a plain column
update, bypassing the `newRepoUnit` machinery entirely.

## API v1

GitHub-compatible paths per `docs/guidelines-backend.md`, tag `securityAdvisory`:

```
GET    /repos/{owner}/{repo}/security-advisories
POST   /repos/{owner}/{repo}/security-advisories
POST   /repos/{owner}/{repo}/security-advisories/reports
GET    /repos/{owner}/{repo}/security-advisories/{gtsa_id}
PATCH  /repos/{owner}/{repo}/security-advisories/{gtsa_id}
GET    /orgs/{org}/security-advisories
```

Plus two Gitea additions: `GET …/{gtsa_id}.osv.json` and `GET /-/advisories.osv.json` — a full OSV
export of *repository* advisories, which GitHub does not offer at all (only its global DB is
exported). This is the payoff for validating ranges on save.

DTOs in `modules/structs/security_advisory.go`, registered in
`routers/api/v1/swagger/security_advisory.go` and `routers/api/v1/swagger/options.go`; converter
`services/convert/security_advisory.go`. Reuse `AccessTokenScopeCategoryRepository` — no new scope
category, since that enum is `iota`-based and append-only. Lists must paginate and set
`ctx.SetTotalCountHeader(...)`. Run `make generate-swagger` and commit the generated JSON.

## Notifications & webhooks

New `HookEventSecurityAdvisory` following the 11-step recipe: `modules/webhook/type.go` (const +
`AllEvents()`), `SecurityAdvisoryPayload` in `modules/structs/hook.go`, the `payloadConvertor`
method in `services/webhook/payloader.go` **plus all nine providers** (slack, discord, dingtalk,
telegram, msteams, feishu, matrix, wechatwork, packagist) and `general.go`, the notifier in
`services/webhook/notifier.go`, `WebhookForm` in `services/forms/repo_form.go`, `ParseHookEvent` in
`routers/web/repo/setting/webhook.go:160`, and the checkbox in
`templates/repo/settings/webhook/settings.tmpl`. `HookEvents` is stored as JSON, so no migration.

**The webhook fires only on publish and withdraw — never on draft or triage activity, and the payload
carries no embargoed content.** Add the `notify.Notifier` methods plus no-op stubs in
`services/notify/null.go` so the other ten notifiers keep compiling.

Mail: `services/security/notify.go` with a **dedicated recipient resolver**. Do not reuse
`mailIssueCommentToParticipants` (`services/mailer/mail_issue.go:28`) — its recipient set is repo
watchers, which would leak an embargoed advisory. Template `templates/mail/repo/advisory/default.tmpl`
plus a `.devtest.yml`.

## Config & i18n

New `modules/setting/security_advisories.go` (`loadSecurityAdvisoriesFrom`) wired into
`loadCommonSettingsFrom` (`modules/setting/setting.go:116`), documented in
`custom/conf/app.example.ini` as `[security_advisories]` with `ENABLED`,
`DEFAULT_PRIVATE_REPORTING_ENABLED`, `REPORT_RATE_LIMIT`.

Locale keys go in `options/locale/locale_en-US.json` **only** (flat dotted keys, JSON not INI), under
`repo.security_advisories.*`. Frontend strings travel via `ctx.PageData` or `data-*` attributes —
`templates/base/head_script.tmpl:20` explicitly forbids adding feature strings to the global
`window.config.i18n`.

## Out of scope for this milestone

Stated explicitly so nothing is silently dropped: private fix collaboration (embargoed branch or
temporary private fork), the instance vulnerability database and OSV import, dependency alerts,
attachments on advisories (needs an `AdvisoryID` column on `models/repo/attachment.go` plus an ACL
check in `routers/web/repo/attachment.go` — genuinely useful for PoC files, worth a follow-up), UI
bell notifications (`models/activities/notification.go` only has `IssueID`/`CommitID`/`CommentID`, so
this needs a column + a new `NotificationSource`), CVE/CNA integration, and federation. On federation:
ActivityPub was **deleted** from this tree in `1592576fa5` — `routers/api/v1/activitypub/person.go` is
14 lines returning 501. There is nothing to build on and it should not be attempted here.

## Build order

1. `models/security/` + `modules/security/{cvss,versionrange}` + unit tests. No routes yet.
2. `modelmigration/v28/v349.go` for the two `repository` columns; fixtures in `models/fixtures/`.
3. `services/security/{perm,advisory,publish}.go` — ACL first, with tests proving an unpublished
   advisory is invisible to a repo writer who is not a collaborator.
4. Web routes, templates, nav tab, repo setting.
5. Intake: `services/security/{template,report}.go`, default form, duplicate detection, rate limit.
6. API v1 + `make generate-swagger`.
7. OSV export.
8. Webhook + mail.
9. Locale, `app.example.ini`, integration tests.

Steps 1–4 are one reviewable PR; 5, 6+7, and 8 are separate PRs.

## Size

Measured against comparables in this tree. The Actions implementation (`4011821c94`) was **117 files,
7,539 insertions**, though that included the runner protocol, `dbfs`, storage backends and 15 new
deps — an upper bound. Projects, the closest repo-CRUD-with-templates analogue, is ~2,000 lines
(`models/project` 1,049 + `routers/web/repo/projects.go` 499 + `services/projects` 329 + 3 templates
+ 186 TS). This lands nearer Actions: **~90–110 files, ~7,000–9,000 hand-written lines**, plus ~2,000
lines of generated swagger JSON that is committed but not authored.

| PR | Files | Lines | Notes |
|---|---|---|---|
| 1 — models + ACL + web UI | ~45 | ~3,500 | 6 tables, `AccessibleAdvisoryCondition`, 5 templates, nav, repo setting |
| 2 — intake + quality gates | ~8 | ~700 | Small because `modules/issue/template` (633 lines) is reused whole |
| 3 — API v1 + OSV export | ~12 | ~1,300 | `routers/api/v1/repo/issue.go` averages ~130 lines/endpoint with swagger comments |
| 4 — webhook + mail | ~20 | ~600 | Wide but shallow: 9 webhook providers × ~8 lines each |
| tests / locale / config | — | ~1,200 | Spread across all four |

Splitting into four PRs is not optional at this size — the Actions precedent of landing ~100 files in
one commit is the thing to avoid, not to copy.

## Verification

- **Unit:** `go test -run '^TestAdvisory' ./models/security/`,
  `go test -run '^TestCVSS' ./modules/security/cvss/`,
  `go test -run '^TestVersionRange' ./modules/security/versionrange/`. Cover: GTSA ID collision
  retry, CVSS vector→score, range parse/normalize incl. rejecting unbounded, state transitions.
- **ACL (the security-critical test):** an integration test in
  `tests/integration/security_advisory_test.go` asserting a repo *writer* who is not an advisory
  collaborator gets `404` on the detail page, the list, the API, and the OSV export for a `draft`,
  and `200` once `published`. Also assert the org inbox and instance list never contain unpublished
  rows. Per `AGENTS.md`, target sub-2s.
- **Intake:** submit a report against an EOL version → rejected; submit without the AI-disclosure
  checkbox → rejected; submit a near-duplicate → warned.
- **Leak checks:** publish an advisory with a webhook configured and assert the draft-phase payload
  count is zero; assert the mail recipient set excludes plain repo watchers.
- **Manual:** `make watch`, create a repo, enable private reporting, submit a report as a second
  user, triage → draft → publish, confirm the tab appears/disappears correctly for anonymous, writer,
  and admin viewers, and that `/-/advisories.osv.json` validates against the OSV schema.
- **Lint:** `make fmt`, `make lint-go`, `make lint-templates`, `make lint-js`, `make swagger-check`.

## Research appendix — how GitHub's system works

Kept so a fresh session doesn't have to re-derive it.

**Two things share the name.** *Repository advisories* are per-repo objects with states
`draft → triage → published`, plus `closed` and `withdrawn`, created by a maintainer or by a
researcher via Private Vulnerability Reporting (which lands in `triage`). *The GitHub Advisory
Database* is the curated global aggregate that feeds Dependabot; repo advisories flow into it on
publish and it is exported in OSV format.

**REST field model** (`docs.github.com/en/rest/security-advisories/repository-advisories`):
`ghsa_id`, `cve_id`, `summary` (≤1024), `description` (≤65535), `severity`
(critical/high/medium/low), `cvss_severities` (v3 + v4 vectors), `cwe_ids`, `identifiers[]`,
`credits[]` (10 roles, each `accepted|declined|pending`), and
`vulnerabilities[] = {package{ecosystem, name}, vulnerable_version_range, patched_versions,
vulnerable_functions}`. Ecosystem is a closed enum of 13 values.

**Permission model** — the part most people get wrong, and the reason this plan uses a per-object
ACL. Advisory access is *not* derived from repo permissions except at the top. Repo write does
**not** grant draft visibility. Advisory *write* = explicitly-added collaborator (comment, edit
metadata, manage credits, push to the fork). Advisory *admin* = repo/org owner or security manager
(add collaborators, create the private fork, merge, close, publish, request CVE).

**Temporary private fork** (deliberately out of scope here): named
`repo-ghsa-xxxx-xxxx-xxxx`, admin-created. CI and integrations cannot access it — the single
most-cited maintainer complaint. Merging is bulk, only one PR may target `main`, and branch
protection is **not enforced** on that merge.

**Standards worth knowing:** OSV schema (`ossf.github.io/osv-schema`) with
`affected[].ranges[].events[]` of `introduced`/`fixed`/`last_affected`/`limit`; OSV data-quality
guidance (`google.github.io/osv.dev/data_quality.html`) which requires `introduced` always be set and
prefers `fixed` over `last_affected` to minimise false negatives. CVE JSON 5.1 and CSAF 2.0/VEX are
the enterprise-side formats; both are derivable from the model above but neither is needed here.
