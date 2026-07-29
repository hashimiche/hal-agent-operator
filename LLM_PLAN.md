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

**Current position:** `T15 — VSO + Vault (infra / operator split)` — **vague 1 lab mostly done**

> Pause 2026-07-29: VSO Gemini SYNCED; GitHub App plugin + `vault read github/token/triage`
> OK (JIT). Vault lab has `-dev-plugin-dir`. Chart `createSecret: false` (GKE values).
> **Next (2026-07-30):** T15 wave 2 — `VaultDynamicSecret` → Jobs (drop PAT); then OCI
> retag `v0.0.3` (NP missing from `0.0.2`); E2E VSO-only → `PROpen`. Campaign plan:
> Cursor plan `t15_vso_vault_campaign`. WIF = GHA→GCP/GKE only; controller never
> talks to GH/Vault. T14 done; T12 `v0.0.2` on GHCR; T16 `create-cr` still open.

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
| T8 | [x] done | `test-hal-operator` fork | Fixture: 4 bugs + issues #5–#8 (**was BLOCKING**) |
| T9 | [x] done | operator | Controller phases Ready/Executing/PROpen/Failed |
| T10 | [x] done | operator | `cmd/fix` binary + separate fix image |
| T11 | [x] done | operator | Chart + runbook for Job 2 (KinD) |
| T12 | [x] done | operator | CI publish: image GHCR + Helm chart |
| T13 | [x] done | `hal-agent-infra` | TF GKE/Vault/WIF/RBAC + smoke WIF (2026-07-29) |
| T14 | [x] done | operator | Deploy operator on GKE |
| T15 | [~] partial | infra + operator | VSO Gemini + GH App token CLI OK ; DynamicSecret/Jobs + OCI v0.0.3 remain |
| T16 | [~] partial | `hal` fork | Approve workflow done; `create-cr` + env/E2E remain |
| T17 | [ ] todo | operator | Hardening & ops |
| T18 | [ ] todo | operator + infra | Docs (ROADMAP.md + infra README) |

## Context

Triage POC runs: `IssueResolution` CR → triage Job → Gemini → `status.triage`,
phase `PendingValidation`, then `spec.approved` → `Ready`. **Job 2 is wired in
code (T9–T10):** `Ready` → `Executing` → `PROpen` (retries via
`spec.maxFixAttempts`). Secrets for the POC = K8s Secret `gemini-api` (+ GitHub PAT for fix Jobs until
T15). Target state (T15): **Vault Secrets Operator (VSO)** syncs Vault secrets
into K8s Secrets consumed by Jobs via existing `SecretKeyRef` — no init-container
Vault auth in Job pods.

Target: issue GitHub → Action → CR on GKE → triage Job → `"agent go"` → fix Job
→ PR (human merge). Secrets via Vault + VSO. Passwordless GHA→GKE auth via WIF
(OIDC); GitHub tokens JIT from Vault (GitHub App + dynamic secrets plugin).

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
- **Fixture repo** (`hashimiche/test-hal-operator`, T8) — injected bugs,
  RED tests, GitHub issues for Job 2 development (not the product `hal` tree).
- **Target / issues repo** (`hal` / fork `hashimiche/hal`) — business events:
  `issues.opened` + `"agent go"` workflows, CODEOWNERS, approval runbook (T16).

The operator **consumes** the cluster (Helm + values); the infra **provisions**
it. Terraform outputs (`cluster_name`, `wif_provider`, `vault_addr`, …) live in
`hal-agent-infra` and are referenced by workflows / Helm values.

```mermaid
flowchart LR
  subgraph local [KinD first]
    F[T8 fork bugs done]
    J2[T9 T10 Job 2 done]
    Chart[T11 chart runbook]
    Pub[T12 CI GHCR Helm]
  end
  subgraph infraRepo [hal-agent-infra]
    TF[T13 TF GKE Vault WIF]
  end
  subgraph cloud [On GKE]
    Deploy[T14 Helm operator]
    GHA[T16 Actions]
    Vlt[T15 VSO Vault sync]
  end
  F --> J2 --> Chart --> Pub
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

# Done — Job 2 foundation (T8–T10)

> Plan docs: [`docs/plans/T8-fixture-fork-bugs.md`](docs/plans/T8-fixture-fork-bugs.md),
> [`docs/plans/T9-T10-job2-fixer-architecture.md`](docs/plans/T9-T10-job2-fixer-architecture.md).

## T8 — Fixture: fork + inject bugs (done)

**Repo**: `hashimiche/test-hal-operator` (fixture fork; **not** the operator repo
and **not** the product `hal` tree). Plan lived in this operator repo.

**Landed**:

- Merged PR #4: test seams (`CheckContainer` / `CheckMultipass` package vars,
  `MariaDBEndpoint()`, `VaultAttachEndpoint()`).
- Branches `fixture/bug1`–`fixture/bug4` with injected bugs + RED tests.
- GitHub issues #5–#8 (symptom-only, `bug` label).
- No `bugfix/**` branches (Job 2 opens those).

**Acceptance met**: 4 reproducible bugs; `go test` red on each fixture branch;
issues created on the fork. Wiring Job 2 to clone base `fixture/bug<N>` (not
`main`) remains a T11 runbook item.

---

## T9 — Controller: Job 2 phases (done)

**Repo**: operator (`hal-k8s-operator`).

**Landed**:

- Stub `"POC stops after triage; Job 2 not wired"` removed.
- `Ready` → `Executing` → `PROpen` with retries via `spec.maxFixAttempts`.
- Helpers: `reconcileFix` / `handleFixFailure` / `buildFixJob` / `readFixResult`.
- `task test` passed (11/11 controller specs).

**Note**: implementation may still be **uncommitted** locally in the operator
repo — commit when ready; do not assume it is on remote `main`.

---

## T10 — `cmd/fix` binary (done)

**Repo**: operator (`hal-k8s-operator`).

**Landed**:

- `cmd/fix` pipeline: clone → `go test` → 2-phase Gemini → retest →
  commit/push → PR → termination-log.
- Separate fix image (`golang:1.26`); Helm/Taskfile wiring; shared
  `internal/gemini`.
- Distroless operator image keeps `/manager` + `/triage`; fix Job uses the
  Go toolchain image.

**Note**: same as T9 — code may be uncommitted locally. Full KinD E2E
(triage → approve → Job 2 → PR) is **T11**.

**Side delivery (post-T10, before T11) — [x] done (KinD 2026-07-27)**: Job 1
posts a GitHub triage comment + labels (`triage:executed`, `suspicious:*`,
`in-scope:*`, `agent:pending-validation` / `agent:rejected`) and sets
`status.plan.commentURL`. `kind-poc-helm` wires `github.token` → Secret
`github-pat` (`issues:write`). Validated: CR → triage → comment + labels on
fixture issue.

---

# Remaining — autonomous GKE agent (T11–T18)

## T11 — Chart + runbook for Job 2 (KinD) (done)

**Files**: [`charts/hal-k8s-operator/`](charts/hal-k8s-operator/),
[`POC.md`](POC.md), [`Taskfile.yml`](Taskfile.yml),
[`docs/plans/T11-chart-runbook-job2-kind.md`](docs/plans/T11-chart-runbook-job2-kind.md).

**Depends on**: T8 fixture + T9/T10 code (commit T9–T10 if still local-only).

**To do**:

- values for the Job 2 image/entrypoint, the GitHub Secret, `runtimeClassName`
  (empty by default) — extend whatever T10 already wired.
- Extend POC.md / Taskfile: full flow triage → `kubectl patch approved` →
  Job 2 → PR on `test-hal-operator`, cloning base `fixture/bug<N>`.
- Document per-bug test commands from the T8 fixture doc.

**Acceptance**: on KinD, manual CR → triage → approve → Job 2 → PR on the
fixture fork, following POC.md with no deviation.

**Note (2026-07-28)**: KinD E2E OK — #5/#6 → `PROpen`; #8 multi-file stays RED
(by design). Hardening along the way: surgical Gemini edits, CoreDNS IPv4,
podman image load one-by-one, job2 samples pre-approved.

---

## T12 — CI publish: image GHCR + Helm chart

**Repo**: operator. **Files**: `.github/workflows/*` (new; today lint/test/e2e
only).

**To do**:

| Workflow | Trigger | Output |
|---|---|---|
| Build & push image | tag `v*` / release | `ghcr.io/<org>/hal-k8s-operator:<tag>` |
| Package & push Helm | same | OCI chart `oci://ghcr.io/<org>/charts` |

- Also publish the **separate fix image** (T10) alongside the operator image.
- `permissions: packages: write` (+ `id-token` if needed); GHCR login.
- Chart version aligned to the tag; `appVersion` = image tag/digest.
- amd64 to start (multi-arch later).
- Document install `helm install … oci://…`.

**Acceptance**: a tag pushes image(s) + chart; `helm install` of a published
version works on a fresh cluster (KinD first).

**Note (2026-07-29)**: Publish workflow green on tag `v0.0.2` (fixed invalid
action SHAs). Artifacts: `ghcr.io/hashimiche/hal-k8s-operator(:-fix):v0.0.2`
+ `oci://ghcr.io/hashimiche/charts/hal-k8s-operator` `0.0.2`.

---

## T13 — Repo `hal-agent-infra` (IaC + trust boundaries)

**Repo**: **`hal-agent-infra`** (new). Do **after** T11 validated locally
(avoid paying for GKE during the fix iteration). **All** Terraform lives here —
no `.tf` in `hal-k8s-operator`.

**Done** (2026-07-29, POC / short-lived GCP project):

- Layout `modules/{gke,wif,vault,rbac}` + `envs/{bootstrap,lab}`; README trust
  model + outputs. **Local Terraform state** (GCS optional, not required for
  POC). TF apply is **local only** — GitHub Actions keeps `smoke-wif.yml` only
  (no TF CI workflow).
- `envs/bootstrap`: runner SA + IAM + APIs; lab apply via
  `GOOGLE_IMPERSONATE_SERVICE_ACCOUNT` (no JSON key).
- TF scope delivered:
  1. **GCP WIF + SA** — GitHub OIDC pool/provider; condition
     `attribute.repository` (+ environment); deployer SA with limited GKE rights.
  2. **GKE** — Standard cluster + node pool; Workload Identity; namespace
     `hal-agent`.
  3. **Vault** — Helm + Job SAs; kubernetes auth/policies via second apply
     (`vault_configure_auth`).
  4. **K8s RBAC for the GHA runner** — namespaced Role/RoleBinding:
     `create/get/patch` on `issueresolutions` only; **no** verbs on `status`.
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

> **Superseded at T15:** Jobs no longer authenticate to Vault directly.
> **Vault Secrets Operator** syncs secrets into K8s Secrets; see T15 and
> `docs/operator-architecture.md` §6.

**Acceptance** ✅: `terraform apply` OK; smoke `workflow_dispatch` (OIDC → WIF →
`kubectl get ns` / soft `kubectl get issueresolutions -n hal-agent`) succeeded
**without a JSON key** (2026-07-29).

---

## T14 — Deploy the operator on GKE

**Repo**: operator, consuming `hal-agent-infra` outputs.
**Runbook**: [`docs/plans/T14-deploy-operator-gke.md`](docs/plans/T14-deploy-operator-gke.md).

**To do**:

- `helm upgrade --install` (GHCR image / OCI chart from T12).
- Namespace `hal-agent` (created by TF or the chart); CRDs; controller RBAC.
- Secrets still K8s if Vault not ready (switch at T15).
- NetworkPolicy for Jobs: egress GitHub + LLM endpoint only (architecture §6) —
  manifests in the chart / overlay, no application TF.

**Acceptance**: KinD scenario replayed on GKE with a manual CR → triage →
approve → fix → PR.

**Note (2026-07-29)**: GKE lab OK — Helm `hal-agent` + GHCR `v0.0.2`, Calico NP
(DNS allow ClusterIP pre-DNAT), samples `job2/issue-5` (+ #6) → `PROpen`.
Secrets still K8s until T15. Runbook: `docs/plans/T14-deploy-operator-gke.md`.

---

## T15 — VSO + Vault (infra / operator split)

**Decision (2026-07-29):** secret delivery = **Vault Secrets Operator (VSO)**,
not init-container Vault auth in Job pods. VSO is a **cluster component** (Helm
in `hal-agent-infra`); it is **not** bundled in the `hal-k8s-operator` image.
Jobs keep `SecretKeyRef` — **zero controller change** if Secret names/keys stay
stable (`gemini-api`, `github-triage` / `github-fix` or chart defaults).

**To do**:

| Layer | Work |
|---|---|
| **`hal-agent-infra`** | Install VSO (Helm); `VaultConnection` + `VaultAuth` (kubernetes auth for VSO SA); policies. **Gemini:** KV (`hal-agent/llm`) → `VaultStaticSecret` → K8s Secret `gemini-api`. **GitHub:** create GitHub App; lodge App ID + private key in Vault; enable [`vault-plugin-secrets-github`](https://github.com/martinbaillie/vault-plugin-secrets-github) (or maintained fork) → `VaultDynamicSecret`(s) → K8s Secrets for triage/fix (scopes: Job 1 `issues:write`; Job 2 `contents:write` + `pull_requests:write`). NP: VSO pods → Vault egress; Job NP unchanged (GitHub + LLM only — Jobs **never** contact Vault). |
| **`hal-k8s-operator`** (chart/values/docs) | `createSecret: false` for GKE (no `--set gemini.apiKey` / `github.token` plaintext); document that Secrets come from VSO CRs. Retag/publish chart if Job NP missing from OCI `0.0.2`. |
| **Controller** | **No** Vault/GitHub client (golden rule). Still creates Jobs with `SecretKeyRef` only. |

**Trust boundaries (unchanged / clarified):**

- **WIF** = GitHub Actions → GCP/GKE only (not operator ↔ GitHub).
- **GitHub tokens** = GitHub App JWT → installation token short-TTL via Vault
  plugin — not WIF, not a long-lived PAT in Helm.
- Job SAs: `automountServiceAccountToken: false`, no K8s API rights — they read
  synced Secrets only; they do not authenticate to Vault.

**Campaign order** (see plan `t15_vso_vault_campaign`): (1) Helm OCI from GHCR,
(2) WIF smoke (already OK), (3) VSO + Gemini static secret, (4) GitHub App +
`VaultDynamicSecret`, (5) E2E GKE with VSO-only secrets → `PROpen`.

**Acceptance**:

- Jobs succeed with **no plaintext secret** in Helm values, CR, or controller
  logs.
- Gemini key and GitHub tokens arrive via VSO-synced K8s Secrets (not chart
  `createSecret`, not init-container).
- Job 1 comments/labels; Job 2 opens PR **without** a long-lived PAT in cluster
  or Helm; GitHub token TTL short and renewable (Vault/VSO observable).
- Controller still never calls Vault or GitHub.

**Trade-off vs init-container:** GitHub token lives in a K8s Secret (namespace
RBAC must stay tight) rather than only on an ephemeral `emptyDir`. Accepted for
VSO simplicity and stable `SecretKeyRef` contract.

---

## T16 — GitHub Actions (target repo) — **partial**

**Repo**: issues repo (`hashimiche/hal`), not the operator (architecture §2).
Plan: [`docs/plans/T16-github-k8s-approval-workflow.md`](docs/plans/T16-github-k8s-approval-workflow.md).
Base on [`config/samples/issueresolution.template.yaml`](config/samples/issueresolution.template.yaml).

### Done (approve volet)

- `.github/workflows/agent-approve.yml` + `docs/agent-approval-runbook.md` in the
  `hal` fork.
- Triggers: comment `agent go` or label `agent: go`; OIDC → WIF → GKE; patch
  `spec.approved` only (never `status.*`).

### Remaining

| Item | Notes |
|---|---|
| Manual env/vars | Protected env `hal-cluster`, GCP/GKE/`HAL_AGENT_NAMESPACE` vars, label `agent: go`, merge workflow to `main` |
| E2E smoke | CODEOWNER `agent go` → CR `spec.approved=true` on cluster (needs T13/T14 for real GKE) |
| **`create-cr` workflow** | Still **open** / separate deliverable: `issues.opened` → fill template → `kubectl apply` CR `issue-<n>` |

**Full acceptance** (when `create-cr` + infra are ready): open an issue on the
fork → CR on GKE → triage → `"agent go"` → Job 2 → PR; human merge only.

---

## T17 — Hardening & ops (post autonomous MVP)

**To do**:

- Monitoring: CR conditions, alerts on `Failed` Jobs, controller metrics.
- Quotas / `maxFixAttempts`, blacklist (`AgentPolicy` later).
- Incident runbooks: revoke Vault role / WIF SA (procedures in
  `hal-agent-infra`).
- Dashboard: **deferred** (primary gate = GitHub comment).
- **Nice-to-have (CI UX):** merge Kubebuilder `lint.yml` / `test.yml` /
  `test-e2e.yml` into one workflow with 3 parallel jobs — today a one-file
  lint fix still fires three separate Actions (same `push`/`pull_request`
  triggers). Keep `publish.yml` separate (tag/release only).

**Acceptance**: reproducible end-to-end on GKE + basic alerting in place.

---

## T18 — Docs

**To do**:

- [`ROADMAP.md`](ROADMAP.md) at the operator root = the T8–T18 view, linked from
  [`LLM_CONTEXT.md`](LLM_CONTEXT.md) and [`README.md`](README.md). *(This file,
  `LLM_PLAN.md`, is the live tracker; ROADMAP.md can be a stable public view.)*
- Mirror README in `hal-agent-infra` (outputs, trust model, smoke WIF).

**Acceptance**: a newcomer follows LLM_PLAN.md T11→T17 with no oral context.

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
