# LLM_PLAN — plan of record (resume here)

> **Single source of truth for progress.** Any agent/model resumes work from this
> file. Merges the triage POC plan (T0–T7, done) and the roadmap to an autonomous
> GKE agent (T8–T18).
>
> Read first: [`LLM_CONTEXT.md`](LLM_CONTEXT.md), [`AGENTS.md`](AGENTS.md),
> [`docs/operator-architecture.md`](docs/operator-architecture.md).
> Kubebuilder rule: never hand-edit generated files (`zz_generated.*`,
> `config/crd/bases/*`, `config/rbac/role.yaml`) — use `task manifests` /
> `task generate`.

## How to use this file (for agents)

1. Read the **Progress tracker** below; the first `[ ]` task is where to resume.
2. Do that task only, respecting its **Files / To do / Acceptance**.
3. Run the validation command; when acceptance is met, flip its `[ ]` → `[x]`,
   update the status column, and move the **Current position** pointer.
4. Never skip a `BLOCKING` task. Never edit generated files by hand.

**Current position:** `T8 — Fixture (fork hal + inject bugs)`

## Progress tracker

| Task | Status | Repo | Summary |
|---|---|---|---|
| T0 | [x] done | operator | Anthropic → Gemini migration |
| T1 | [x] done | operator | `readTriageResult`: only read succeeded pod |
| T2 | [x] done | operator | Technical error ≠ business rejection |
| T3 | [x] done | operator | Replace deprecated `Requeue: true` |
| T4 | [x] done | operator | Deduplicate default model ID |
| T5 | [x] done | operator | Missing tests (coverage ≥ 70%) |
| T6 | [x] done | operator | Verify Taskfile podman flow |
| T7 | [x] done | operator | End-to-end triage validation on KinD |
| T8 | [ ] todo | `hal` fork | Fixture: fork + 3 injected bugs (**BLOCKING**) |
| T9 | [ ] todo | operator | Controller phases Ready/Executing/PROpen/Failed |
| T10 | [ ] todo | operator | `cmd/fix` binary |
| T11 | [ ] todo | operator | Chart + runbook for Job 2 (KinD) |
| T12 | [ ] todo | operator | CI publish: image GHCR + Helm chart |
| T13 | [ ] todo | `hal-agent-infra` | TF GKE/Vault/WIF/RBAC + smoke WIF |
| T14 | [ ] todo | operator | Deploy operator on GKE |
| T15 | [ ] todo | infra + operator | Vault integration |
| T16 | [ ] todo | `hal` fork | Actions: `issues.opened` + `"agent go"` |
| T17 | [ ] todo | operator | Hardening & ops |
| T18 | [ ] todo | operator + infra | Docs (ROADMAP.md + infra README) |

## Context

Triage POC runs: `IssueResolution` CR → triage Job → Gemini → `status.triage`,
phase `PendingValidation`, then `spec.approved` → `Ready`. The controller
**stops there** with *"POC stops after triage; Job 2 not wired"*. Secrets =
K8s Secret `gemini-api`.

Target: issue GitHub → Action → CR on GKE → triage Job → `"agent go"` → fix Job
→ PR (human merge). Secrets via Vault. Passwordless auth GH↔GCP/GKE and
Jobs↔Vault (OIDC / Workload Identity Federation).

Command entry point: **`task`** (go-task, [`Taskfile.yml`](Taskfile.yml)). The
Makefile is the internal Kubebuilder engine — go through `task`.

Validation command after **every** task touching the operator:

```bash
task lint-fix && task test
```

Global criterion: `task test` green, no generated file hand-edited, no secret
written to any file.

## Repo split

- **`hal-k8s-operator`** (this repo) — operator product: Go, CRD, Helm chart,
  image, publish CI, KinD POC. **No `.tf` here.**
- **`hal-agent-infra`** (new, T13) — all IaC: Terraform GKE, Vault, GCP WIF, SA,
  cluster RBAC, smoke WIF.
- **Target repo** (`hal` / fork) — business events: `issues.opened` +
  `"agent go"` workflows, CODEOWNERS, fixture issues.

The operator **consumes** the cluster (Helm + values); the infra **provisions**
it. Terraform outputs (`cluster_name`, `wif_provider`, `vault_addr`, …) live in
`hal-agent-infra` and are referenced by workflows / Helm values.

```mermaid
flowchart LR
  subgraph local [KinD first]
    F[T8 fork bugs]
    J2[T9 T10 Job 2 fix]
    Pub[T12 CI GHCR Helm]
  end
  subgraph infraRepo [hal-agent-infra]
    TF[T13 TF GKE Vault WIF]
  end
  subgraph cloud [On GKE]
    Deploy[T14 Helm operator]
    GHA[T16 Actions issue CR]
    Vlt[T15 Vault auth Jobs]
  end
  F --> J2 --> Pub
  J2 --> TF --> Deploy --> GHA
  Deploy --> Vlt
```

Target state machine (`status.phase`):

```
Triage → PendingValidation → Ready → Executing → PROpen → Done
   |          (done)           |
   v                          v
Rejected                   Failed
```

---

# Done — triage POC (T0–T7)

> Validated end-to-end on KinD, 2026-07-19 (`task test` green, controller
> coverage 74.6%). Kept condensed for history; full narrative was in the
> original `GROK_PLAN.md`.

- **T0 — Anthropic → Gemini migration (was BLOCKING).** Replaced the Claude API
  call with Gemini (Google AI Studio key) in `cmd/triage/main.go`, controller,
  `cmd/main.go`, Helm chart. Env `GEMINI_API_KEY` / `GEMINI_MODEL`; default
  model in `internal/defaults/defaults.go`.
  *Hotfix 2026-07-19:* Google retired `gemini-2.5-flash` for new keys; default
  moved to the rolling alias `gemini-flash-latest`.
- **T1 — `readTriageResult`: only the succeeded pod.** Skip containers with
  `ExitCode != 0` so a failed attempt's termination-log is not read.
- **T2 — Technical error ≠ business rejection.** Added `parseError`; unreadable
  termination-log / unparsable JSON → `Failed` (not `Rejected`). `Rejected`
  stays for `Suspicious || !InScope` on a valid result.
- **T3 — Deprecated `Requeue`.** `ctrl.Result{Requeue: true}` →
  `RequeueAfter: time.Second` (controller-runtime v0.24).
- **T4 — Deduplicate default model ID.** `internal/defaults/defaults.go` is the
  single Go source of the model constant.
- **T5 — Missing tests.** `parseResult`, `truncateRunes`, envtest reject/fail
  paths, T1/T2 cases. `internal/controller` coverage ≥ 70%.
- **T6 — Taskfile podman flow.** `task kind-poc-cluster` / `kind-poc-image`
  verified on podman-only (WSL2).
- **T7 — End-to-end validation.** POC.md steps 1→5 on a clean KinD: CR → triage
  Job → Gemini analysis in logs → `status.triage.summary` filled →
  `PendingValidation`.

---

# Remaining — autonomous GKE agent (T8–T18)

## T8 — Fixture: fork `hal` + inject bugs (BLOCKING)

**Repo**: `hal` fork (outside the operator). **Blocks T9/T10**: no fix loop
without a target repo, a known bug and a red test.

**To do**:

- Fork `hal` (or lab clone) + **3 bugs** of increasing difficulty:
  typo/off-by-one → logic bug in an isolated function → bug spanning 2 files
  (shows the limit of the single-file approach).
- Per bug: GitHub issue + failing test + success criterion (test green + PR).
- Document the fixture (paths, commands) for the Job 2 runbook.

**Acceptance**: 3 reproducible bugs; `go test` red on each before fix; issues
created on the fork.

---

## T9 — Controller: Job 2 phases (Ready/Executing/PROpen/Failed)

**File**: [`internal/controller/issueresolution_controller.go`](internal/controller/issueresolution_controller.go)
(remove the stub `case PhaseReady, PhaseExecuting, PhasePROpen, PhaseDone`).

**To do** (structural mirror of `reconcileTriage`, ~200 lines):

- `Ready` → create fix Job `issue-<n>-fix-<attempt>` with `OwnerReference` if
  absent.
- `Executing` → watch the Job; requeue.
- `PROpen` → read termination-log → fill `status.execution` (`prURL`,
  `prNumber`, `branch`, `attempt`).
- Read `spec.maxFixAttempts` (present in the CRD, **never read** today) to drive
  retry; Job `Failed` + attempts exhausted → `PhaseFailed` + readable condition.
- Never relaunch a Job already `Succeeded` for the same phase (idempotency).

**Acceptance**: envtest — (a) `Ready` → fix Job created with OwnerRef; (b) Job
succeed → `PROpen` + `status.execution.prURL`; (c) Job fail beyond
`maxFixAttempts` → `Failed`. `task test` green.

---

## T10 — `cmd/fix` binary

**Files**: `cmd/fix/main.go` (new), [`Dockerfile`](Dockerfile) (add `/fix`
alongside `/manager` + `/triage`).

**To do**:

- clone → code context → LLM prompt (**output = full corrected file, not a
  diff**: LLM diffs fail to apply) → write file → `go test` → commit/push branch
  → open PR → write result (`prURL`, `prNumber`, `branch`) to termination-log.
- POC secrets: K8s Secret (fine-grained PAT scoped to the fork only,
  `contents:write` + `pull_requests:write`, never merge/admin) — **not Vault**
  yet.
- `go mod tidy`; check the distroless image still builds.

**Acceptance**: on a T8 bug, `cmd/fix` locally opens a PR that turns the test
green; termination-log holds a valid `prURL` JSON.

---

## T11 — Chart + runbook for Job 2 (KinD)

**Files**: [`charts/hal-k8s-operator/`](charts/hal-k8s-operator/),
[`POC.md`](POC.md), [`Taskfile.yml`](Taskfile.yml).

**To do**:

- values for the Job 2 image/entrypoint, the GitHub Secret, `runtimeClassName`
  (empty by default).
- Extend POC.md / Taskfile: full flow triage → `kubectl patch approved` →
  Job 2 → PR.

**Acceptance**: on KinD, manual CR → triage → approve → Job 2 → PR on the fork,
following POC.md with no deviation.

---

## T12 — CI publish: image GHCR + Helm chart

**Repo**: operator. **Files**: `.github/workflows/*` (new; today lint/test/e2e
only).

**To do**:

| Workflow | Trigger | Output |
|---|---|---|
| Build & push image | tag `v*` / release | `ghcr.io/<org>/hal-k8s-operator:<tag>` |
| Package & push Helm | same | OCI chart `oci://ghcr.io/<org>/charts` |

- `permissions: packages: write` (+ `id-token` if needed); GHCR login.
- Chart version aligned to the tag; `appVersion` = image tag/digest.
- amd64 to start (multi-arch later).
- Document install `helm install … oci://…`.

**Acceptance**: a tag pushes image + chart; `helm install` of a published
version works on a fresh cluster (KinD first).

---

## T13 — Repo `hal-agent-infra` (IaC + trust boundaries)

**Repo**: **`hal-agent-infra`** (new). Do **after** T9–T11 validated locally
(avoid paying for GKE during the fix iteration). **All** Terraform lives here —
no `.tf` in `hal-k8s-operator`.

**To do**:

- Bootstrap: README (trust model, GCP/org prerequisites, consumed outputs),
  layout `modules/{gke,vault,wif}` + `envs/lab`, remote state (GCS), CI
  `terraform fmt/validate/plan` (manual apply / protected environment).
- Reuse / module-ize the GKE Terraform already present on the Hashicorp academy
  side (don't blindly duplicate).
- TF scope:
  1. **GCP WIF + SA** — GitHub OIDC pool/provider; condition
     `attribute.repository` (+ environment); SA with limited GKE rights.
  2. **GKE** — **Standard** cluster + node pool; Workload Identity; base
     network; namespace `hal-agent`.
  3. **Vault** — `kubernetes` auth; Job1/Job2 roles; engines / dynamic GitHub
     App or PAT + LLM; least-privilege policies.
  4. **K8s RBAC for the GHA runner** — namespaced Role/RoleBinding:
     `create/get/patch` on `issueresolutions` only; **no** verbs on `status`;
     no Secrets/Vault access.
  5. **Outputs** — `cluster_name`, `cluster_location`, `project_id`,
     `wif_provider`, `deployer_sa_email`, `vault_addr`, `hal_agent_namespace`.

Identity chain (no long-lived secret):

```mermaid
sequenceDiagram
  participant GHA as GitHub_Actions
  participant WIF as GCP_WIF
  participant GKE as GKE_API
  participant Job as K8s_Job
  participant Vault as Vault

  GHA->>WIF: OIDC token repo+env
  WIF->>GHA: short-lived SA token
  GHA->>GKE: kubectl apply/patch IssueResolution
  Job->>Vault: auth/kubernetes SA JWT
  Vault->>Job: dynamic GH token + LLM key
```

**Acceptance**: `terraform apply` OK; smoke `workflow_dispatch` (OIDC → WIF →
`kubectl get ns` / `kubectl get issueresolutions -n hal-agent`) succeeds
**without a JSON key**.

---

## T14 — Deploy the operator on GKE

**Repo**: operator, consuming `hal-agent-infra` outputs.

**To do**:

- `helm upgrade --install` (GHCR image / OCI chart from T12).
- Namespace `hal-agent` (created by TF or the chart); CRDs; controller RBAC.
- Secrets still K8s if Vault not ready (switch at T15).
- NetworkPolicy for Jobs: egress GitHub + LLM endpoint only (architecture §6) —
  manifests in the chart / overlay, no application TF.

**Acceptance**: KinD scenario replayed on GKE with a manual CR → triage →
approve → fix → PR.

---

## T15 — Vault (infra / operator split)

**To do**:

- **`hal-agent-infra`**: finalize Kubernetes auth, roles, policies, engines (if
  not already at T13).
- **`hal-k8s-operator`** (chart): **init-container** pattern Vault → shared
  volume; annotated Job SAs; remove/neutralize POC Secrets (`gemini-api`, PAT).
- Job 1: LLM key (+ issue-read token if GitHub comment/label enabled).
- Job 2: token `contents:write` + `pull_requests:write` + LLM.
- Controller: **never** a Vault/GitHub client (golden rule).

**Acceptance**: Jobs OK with no plaintext secret in Helm values; no secret in
the CR or controller logs.

---

## T16 — GitHub Actions (target repo)

**Repo**: issues repo (`hal` / fork), not the operator (architecture §2).
Base on [`config/samples/issueresolution.template.yaml`](config/samples/issueresolution.template.yaml).

**To do**:

| Workflow | Event | Action |
|---|---|---|
| `create-cr` | `issues.opened` | parse issue → fill template (`gh`/`yq`) → `kubectl apply` CR `issue-<n>` (body truncated ~16KiB) |
| `approve` | `issue_comment` | exact body `"agent go"` + CODEOWNERS author → patch `spec.approved=true` (+ `approvedBy`/`approvedAt`) |

Common: auth **OIDC → WIF → GKE** (T13 provider/SA); protected environment;
idempotency `metadata.name = issue-<number>`; never write `status.*`.

Job 1 complements (if not done earlier): plan comment + label
`agent: pending-validation` (triage is analyze-only today).

**Acceptance**: open an issue on the fork → CR on GKE → triage → `"agent go"` →
Job 2 → PR; human merge only.

---

## T17 — Hardening & ops (post autonomous MVP)

**To do**:

- Monitoring: CR conditions, alerts on `Failed` Jobs, controller metrics.
- Quotas / `maxFixAttempts`, blacklist (`AgentPolicy` later).
- Incident runbooks: revoke Vault role / WIF SA (procedures in
  `hal-agent-infra`).
- Dashboard: **deferred** (primary gate = GitHub comment).

**Acceptance**: reproducible end-to-end on GKE + basic alerting in place.

---

## T18 — Docs

**To do**:

- [`ROADMAP.md`](ROADMAP.md) at the operator root = the T8–T18 view, linked from
  [`LLM_CONTEXT.md`](LLM_CONTEXT.md) and [`README.md`](README.md). *(This file,
  `LLM_PLAN.md`, is the live tracker; ROADMAP.md can be a stable public view.)*
- Mirror README in `hal-agent-infra` (outputs, trust model, smoke WIF).

**Acceptance**: a newcomer follows LLM_PLAN.md T8→T17 with no oral context.

---

## Out of scope (do not do)

- Auto-merge of PRs.
- Webhook receiver / ingress HMAC (decision 2026-07-20: replaced by GHA).
- Sysbox / Autopilot nested containers (not needed while `go test` is
  in-process).
- GitHub/Vault calls from the controller process.
- Terraform in `hal-k8s-operator` (reserved for `hal-agent-infra`).
- Secrets in CRs, committed Helm values, code or logs.
- Editing generated files listed in `AGENTS.md`.
