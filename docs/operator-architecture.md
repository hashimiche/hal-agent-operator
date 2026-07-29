# AI K8s Operator Architecture (HAL project)

> **Status:** Design · **Scope:** this repo (`hal-k8s-operator`)
> Complements [`hal/docs/agent-architecture.md`](../../hal/docs/agent-architecture.md) §16 (Option 4).
> This document fixes the **operator contract** and the operational HITL workflow.

---

## 1. Overall architecture model

Event-driven approach + ephemeral Kubernetes resources, Controller/Operator pattern:

- The operator holds state declaratively through a Custom Resource (CR).
- The operator creates Kubernetes Jobs (Job 1 triage, Job 2 fix) and watches them natively through an `OwnerReference`.
- The reconcile loop is triggered when a Job terminates (`Succeeded` / `Failed`) — no internal webhooks needed to detect Job state changes.

```mermaid
flowchart TD
    GH[GitHub issue / comment] --> GHA[GitHub Action]
    GHA -->|kubectl apply / patch CR| CR[IssueResolution]
    CTRL[Operator reconcile] -.watch CR + owned Jobs.-> CR
    CTRL -->|phase needs triage| J1[Job 1: triage]
    J1 -->|OwnerRef + termination-log| CTRL
    CTRL -->|await human| WAIT[phase = PendingValidation]
    GHA -->|comment "agent go" + CODEOWNERS OK| CR
    CTRL -->|phase = Ready| J2[Job 2: fix]
    J2 -->|OwnerRef + result| CTRL
    CTRL --> PR[phase = PROpen]
```

---

## 2. Who does what (operator boundary)

| Component | Responsibility | In this repo? |
|---|---|---|
| **GitHub Action** | On `issues.opened`: create the CR. On `issue_comment` `"agent go"`: check CODEOWNERS, then patch `spec.approved=true`. Authenticates to the cluster via OIDC / Workload Identity Federation | No (workflows live in the target repo) |
| **Operator (controller)** | Reconcile `IssueResolution`: spawn/watch Jobs, advance `status.phase`, never handle secrets | **Yes — the core of this repo** |
| **Job 1 (triage)** | Text-only analysis, comments the plan, applies label `agent: pending-validation`, writes the diagnosis, exits | Image / entrypoint defined here; runs as a Job |
| **Job 2 (fix)** | Generates code, runs the tests, pushes + opens PR; secrets via K8s Secret (KinD POC) then VSO-synced Secrets from Vault (T15) | Same |
| **Vault Secrets Operator (VSO)** | Syncs Vault secrets into K8s Secrets (`VaultStaticSecret` / `VaultDynamicSecret`); cluster infra in `hal-agent-infra`, not in the operator image | No |
| **Vault** | KV (Gemini key) + GitHub App dynamic tokens (JIT, short TTL) | Cluster infra, outside the operator |
| **Human** | Gate #1: `"agent go"` comment; gate #2: PR review/merge | — |

**Golden rule for the operator:** it never talks to GitHub or Vault. It only reads/writes CRs and Jobs. The Jobs and the GitHub Action are the only Vault/GitHub clients.

> **Decision (2026-07-20) — GitHub Action instead of a Webhook Receiver.**
> The original design called for a publicly exposed receiver Deployment with
> HMAC validation. A GitHub Action makes that whole component unnecessary: no
> ingress, no TLS, no HMAC secret rotation, no service to maintain. The
> direction of the flow inverts (the runner calls the cluster instead of GitHub
> pushing to it), which moves the security problem from "authenticate the
> caller" to "give the runner minimal rights" — see §6.

---

## 3. Human-in-the-Loop (HITL) workflow

### Step 1 — Creation and triage (Job 1)

1. Issue opened → GitHub Action → CR created (`metadata.name = issue-<n>` = dedup).
2. The operator launches **Job 1** (triage).
3. Job 1 analyzes the issue **text-only** (no code execution), comments the action plan, applies the GitHub label `agent: pending-validation`, writes the diagnosis (e.g. `/dev/termination-log`), and exits.
4. The operator detects Job completion (`OwnerReference` → reconcile), reads the diagnosis, updates the CR `status` → phase **PendingValidation**.

### Step 2 — Human barrier

- The issue waits. **No idle pod.**
- A human reviews the plan and, if confident, comments: `"agent go"`.

### Step 3 — Admission control (GitHub Action — outside the operator)

1. The comment triggers the `issue_comment` workflow in the target repo.
2. The Action checks that the comment body is `"agent go"` and that its author is in `CODEOWNERS` (reading the file + the `author_association` carried by the event).
3. It authenticates to GCP via **OIDC / Workload Identity Federation** — no long-lived secret stored in the repo — and obtains a short-lived GKE credential.
4. If OK → patch the CR: `spec.approved=true` (+ `approvedBy`, `approvedAt`). The operator itself performs the transition to **Ready**.

### Step 4 — Execution (Job 2)

1. The operator sees `Ready` and launches **Job 2**.
2. Job 2 generates the code and runs the target repo's test suite (see §6 for the isolation model chosen).
3. Pushes the branch + opens the PR (final human validation = gate #2).
4. The operator observes Job completion → `status.phase = PROpen` (+ `status.execution.prURL`).

---

## 4. `IssueResolution` CR contract

```yaml
apiVersion: agent.hal.dev/v1alpha1
kind: IssueResolution
metadata:
  name: issue-1234          # = issue number → etcd dedup
spec:
  issueNumber: 1234
  # Desired state written by the GitHub Action (not by the operator)
  approved: false           # true after "agent go" + CODEOWNERS OK
status:
  phase: Triage             # see state machine below
  triage: { inScope: true, suspicious: false, summary: "..." }
  plan:
    commentURL: ""
  execution:
    prURL: ""
  conditions: []            # Triaged, AwaitingApproval, Ready, PROpen, Failed
  observedGeneration: 1
```

> The authoritative shape is [`api/v1alpha1/issueresolution_types.go`](../api/v1alpha1/issueresolution_types.go);
> the snippet above is illustrative and trimmed.

### State machine (`status.phase`)

```
Triage → PendingValidation → Ready → Executing → PROpen → Done
                |                 |
                v                 v
            Rejected           Failed
```

| Phase | Who enters it | Operator action |
|---|---|---|
| `Triage` | CR created | Create Job 1 if absent; wait |
| `PendingValidation` | Job 1 Succeeded + in-scope | **Requeue / no-op** while `spec.approved == false` |
| `Ready` | Operator, after `spec.approved=true` set by the GitHub Action (`"agent go"`) | Create Job 2 |
| `Executing` | Job 2 launched | Watch Job 2 |
| `PROpen` | Job 2 Succeeded | Write `status.execution.prURL`; wait for human merge (optional) |
| `Rejected` | Job 1 out-of-scope / suspicious | Stop |
| `Failed` | Job Failed / invalid diagnosis | Stop + `Failed` condition |

Every `status.phase` transition is **written by the operator**. The GitHub Action
never writes to `status`: it only touches `spec` (CR creation, then
`spec.approved`). That is what keeps the state machine level-triggered and
idempotent.

---

## 5. Reconcile contract (what the operator must do)

On every reconcile, for one `IssueResolution`:

1. Read `status.phase` + owned Jobs (`OwnerReference`).
2. Take the **smallest step** toward the desired state (level-triggered, idempotent).
3. Never re-launch a Job already `Succeeded` for the same phase.
4. On Job `Failed` → `status.phase = Failed` + a readable condition.
5. On Job 1 `Succeeded` → parse the result (termination-log / annotation / result ConfigMap) → `PendingValidation` or `Rejected`.
6. If `PendingValidation` and `!spec.approved` → return + requeueAfter (no pod waiting).
7. If `spec.approved` (or phase `Ready`) → create Job 2 if absent.
8. On Job 2 `Succeeded` → `PROpen` + `prURL`.

**What the operator never does:**

- Call GitHub or Vault
- Hold secrets in `spec` / `status`
- Run generated code inside the controller process
- Idle a worker during the human barrier

---

## 6. Security (constraints the operator must respect)

### Secrets — VSO sync into K8s Secrets (Job side, not controller)

> **Decision (2026-07-29) — VSO, not init-container.** Secret delivery uses
> [Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/platform/k8s/vso)
> (Helm in `hal-agent-infra`). VSO authenticates to Vault (kubernetes auth for
> the VSO ServiceAccount) and syncs into K8s Secrets that Jobs mount via
> `SecretKeyRef`. The controller unchanged: it still references Secret
> names/keys; it never talks to Vault or GitHub.

**Gemini (static):** KV path (e.g. `hal-agent/llm`) → `VaultStaticSecret` →
K8s Secret `gemini-api` (same name/key as KinD POC).

**GitHub (dynamic, JIT):** GitHub App (App ID + private key in Vault) +
[`vault-plugin-secrets-github`](https://github.com/martinbaillie/vault-plugin-secrets-github)
→ `VaultDynamicSecret` → K8s Secret(s) for triage/fix. Vault signs an App JWT
and exchanges an **installation token** with short TTL (~1 h); VSO renews before
expiry. **No long-lived PAT** in Helm or the cluster.

| Job | Token scopes (minimal) |
|---|---|
| Job 1 (triage) | `issues:write` (comment + label) |
| Job 2 (fix) | `contents:write` + `pull_requests:write` (push + PR) — never merge/admin |

**Auth boundaries:**

- **WIF (OIDC)** = GitHub Actions → GCP/GKE only — not operator ↔ GitHub, not
  Vault ↔ GitHub.
- Job ServiceAccounts: **no** Vault kubernetes auth, **no** K8s API rights,
  `automountServiceAccountToken: false`. Jobs read synced Secrets only.
- **No secret in the CR** (readable through RBAC `get` in etcd).

**Trade-off vs init-container:** With init-container, tokens could live only on
an ephemeral `emptyDir` shared volume (never a cluster Secret object). With VSO,
the GitHub token exists as a K8s Secret object — simpler Job spec and stable
`SecretKeyRef`, but **namespace RBAC on `secrets` must stay tight** (VSO writer;
Job pods reader via mount only). Accepted for the lab POC.

### Job 2 isolation

> **Decision (2026-07-20) — no Sysbox for the POC.**
> Sysbox answers one precise need: running **nested containers** without
> `--privileged` (docker build, testcontainers, KinD in a pod). As long as Job 2
> is limited to `git`, an LLM call and a test suite that runs **in-process**
> (`go test`), that need does not exist. And the cost is real: node-level
> installation through a DaemonSet, Ubuntu node images with a compatible kernel,
> and **impossible on GKE Autopilot**.
>
> Verified on the actual target (2026-07-20): the `hal` test suite is plain Go —
> `httptest`, no `testcontainers`, no Docker invocation, ~37 s wall clock.
>
> `runtimeClassName` stays a **configurable** Helm value (empty by default): it
> gets wired back the day Job 2 must launch containers.

What Sysbox would not have solved anyway: Job 2 runs **LLM-written code**, i.e.
untrusted code, whatever the runtime. Dropping Sysbox is not free — its user
namespaces would have mitigated a kernel escape. But that is not the realistic
threat here: the crown jewel in that pod is the **GitHub token**, and the likely
attack is generated code reading an env var and exfiltrating it over HTTP.
Sysbox does nothing against that; the egress allowlist does.

The boundary chosen for the POC is therefore a hardened pod, defense in depth:

- `securityContext`: non-root, `allowPrivilegeEscalation: false`,
  `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`,
  `readOnlyRootFilesystem` except an `emptyDir` workspace.
- **NetworkPolicy:** egress = GitHub + LLM endpoint only. **No** access to the
  Kubernetes API or to Vault from Job pods. VSO (separate Deployment) needs its
  own egress allowlist to Vault — not opened from Job containers.
- Dedicated ServiceAccount with **no rights at all** on the Kubernetes API, `automountServiceAccountToken: false`.
- CPU/RAM limits **and `activeDeadlineSeconds`** (protection against a runaway test loop — size it against the ~37 s baseline above; ~300 s leaves margin).
- **Secrets mount:** KinD POC uses chart-created K8s Secrets (`gemini-api`,
  `github-pat`). **T15 (GKE):** VSO syncs Vault → K8s Secrets; Jobs keep
  `SecretKeyRef` — no init-container Vault auth in the Job pod.

The day the target to fix needs Docker for its tests is the day the Sysbox
question reopens — alongside the alternatives of a dedicated node or an external
runner.

### GitHub Action trust boundary

The Action holds write access to the cluster: this is the new attack surface
introduced in §2, and it must be kept tight.

- RBAC restricted to `create` / `get` / `patch` on `issueresolutions` in the `hal-agent` namespace **only**. Nothing else — no cluster-wide `list`, no `secrets`, no `pods`.
- Auth through **OIDC / Workload Identity Federation**, with a condition on the repo *and* the branch/environment — no service-account JSON key stored as a repo secret.
- The `issue_comment` workflow must never trust the comment body beyond strict equality with `"agent go"`, nor `author_association` alone: the CODEOWNERS check is what carries authority.
- The Action must write to `spec` only — never to `status` (cf. §4).

### Prompt-injection mitigation

- Job 1 = analysis only, zero code execution → an aberrant plan is blocked by the CODEOWNER (`"agent go"`).
- Job 2 only exists after a human gate validated both cryptographically and hierarchically.
- The model never sees the publish GitHub token (separate Secret / scope for fix
  vs triage; short TTL via Vault dynamic secrets).

---

## 7. Surfaces outside the operator's immediate scope

To be built **after** the controller skeleton + CRD + reconcile stub:

- GitHub Action workflows (CR creation + CODEOWNERS + `spec.approved` patch)
- Job 1 / Job 2 images + `CodeFixProvider`
- Vault Secrets Operator + NetworkPolicy manifests (`hal-agent-infra`)
- Dashboard (optional) — the primary gate here is the GitHub `"agent go"` comment, not a UI

---

## 8. Build order

Slices A (skeleton) and B (state machine + real triage) are **done** — POC
validated on KinD on 2026-07-19, see [`POC.md`](../POC.md).

Next, in this order (the dependencies matter):

1. **Fork `hal` + inject bugs.** This is the test fixture: Job 2 cannot be
   developed without a real target repo, a known bug, and a failing test. Aim
   for 3 bugs of increasing difficulty — typo/off-by-one, logic bug in an
   isolated function, bug spanning two files (that last one will show where the
   single-file approach breaks).
2. **Job 2 (fix) on KinD.** The big chunk, but it is developed entirely locally
   — see §9.
3. **GitHub Action workflows.** CR creation on `issues.opened`, approval on
   `issue_comment`.
4. **GKE last.** It is only needed to make the cluster reachable by the GitHub
   runner. It adds nothing to the fix logic, and it costs money while iterating.

---

## 9. Job 2 (fix) breakdown

Two very uneven halves.

**Controller side (~200 lines)** — a structural mirror of `reconcileTriage`:

- `Ready` → create the fix Job if absent (naming `issue-<n>-fix-<attempt>`).
- `Executing` → watch; requeue.
- `PROpen` → read the result from the termination-log, fill `status.execution`.
- Retry driven by `spec.maxFixAttempts` — a field already present in the CRD but
  **never read** by the controller today.

**`cmd/fix` binary side (the real work)**: clone → LLM prompt with code context →
apply the change → run the tests → commit/push → open the PR → write the URL to
the termination-log.

> **Model output format: full file contents, not a unified diff.**
> LLM-produced diffs carry line numbers and context that do not match, and
> `git apply` fails constantly. Asking for the **full contents of the corrected
> file**, on a single targeted file, is token-verbose but reliable — which is the
> criterion that matters for a demonstrable POC. Moving to multi-file / diff is a
> later evolution.

**Job 2 secrets:**

- **KinD POC:** K8s Secret `github-pat` (fine-grained PAT on the fork) +
  `gemini-api`, created by the chart.
- **GKE / T15:** VSO `VaultStaticSecret` → `gemini-api`; VSO
  `VaultDynamicSecret` → GitHub token Secret(s) from GitHub App + Vault plugin
  (JIT, short TTL). Chart `createSecret: false`; no plaintext in Helm values.
  Controller still uses `SecretKeyRef` — same contract, different provenance.
