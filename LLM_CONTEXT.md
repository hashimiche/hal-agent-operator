# HAL K8s Operator — LLM Context

> Canonical context for humans and LLMs working in this repo.  
> Full narrative design: [`docs/operator-architecture.md`](docs/operator-architecture.md).  
> Parent product design: [`../hal/docs/agent-architecture.md`](../hal/docs/agent-architecture.md) §16.

## What this is

A **Kubernetes operator** that reconciles `IssueResolution` CRs for the HAL
issue-resolver agent. The controller pod runs a Go binary (`controller-runtime`):
it watches the API for CRs (and owned Jobs), and runs **level-triggered**
`Reconcile` to advance a state machine. It never talks to GitHub or Vault.

| Component | Role | This repo? |
|---|---|---|
| Operator (controller) | Watch CRs / Jobs → reconcile phases | **Yes** |
| Webhook receiver | HMAC + create CR / `"agent go"` → `spec.approved` | Later (adjacent) |
| Job 1 triage | Text-only analysis, plan comment, label | Image later |
| Job 2 fix | Sysbox: code + test + PR | Image later |
| Dashboard | Nice-to-have; talks only via CRs | Deferred |

## Core rules

1. **No secrets in CRs** (etcd is RBAC-readable). Secrets = Vault → Jobs only.
2. **Controller never calls GitHub/Vault** — only creates/watches Jobs + patches status.
3. **One CR per issue** (`metadata.name = issue-<number>`) = dedup lock.
4. **Human gate #1:** GitHub comment `"agent go"` by a CODEOWNER → webhook sets
   `spec.approved=true`. No idle pod while waiting.
5. **Human gate #2:** PR review/merge on GitHub. Never auto-merge.
6. Jobs are linked with **OwnerReference** so Job completion re-triggers reconcile.

## CR shape (`IssueResolution`)

```yaml
apiVersion: agent.hal.dev/v1alpha1
kind: IssueResolution
metadata:
  name: issue-1234
  namespace: hal-agent
  labels:
    hal.dev/repo: "owner/hal"
    hal.dev/author: "alice"
spec:
  repository: "owner/hal"
  issueNumber: 1234
  issueURL: "https://github.com/owner/hal/issues/1234"
  author: "alice"
  authorAssociation: "CONTRIBUTOR"
  title: "docs: typo in vault oidc"
  body: |
    Snapshot of issue body (truncate if huge; Job may re-fetch).
  labels: ["bug", "docs"]
  createdAt: "2026-07-18T20:00:00Z"
  approved: false
  approvedBy: ""
  approvedAt: null
  maxFixAttempts: 2
status:
  phase: PendingValidation
  observedGeneration: 1
  triage:
    inScope: true
    suspicious: false
    summary: "Doc typo; safe to auto-fix"
    model: "ollama/llama3"
  plan:
    commentURL: "https://github.com/.../issues/1234#issuecomment-…"
    summary: "Fix wording in skill + EXAMPLES"
  execution:
    attempt: 1
    jobName: "issue-1234-fix-1"
    branch: "bugfix/1234-oidc-typo"
    prURL: ""
    prNumber: 0
  conditions:
    - type: Triaged
      status: "True"
      reason: "InScope"
```

### Who writes what

| Field | Writer |
|---|---|
| `spec.*` (issue snapshot) | Webhook receiver on `issues.opened` |
| `spec.approved` / `approvedBy` / `approvedAt` | Webhook receiver on `"agent go"` + CODEOWNERS |
| `status.*` | Operator (from Job results) |

### Do not put in the CR

- Tokens, LLM keys, HMAC secrets  
- Full build logs / huge diffs  
- Global blacklist (use a separate `AgentPolicy` ConfigMap/CRD later)

### Body size

Keep `spec.body` truncated (e.g. 16KiB) + optional `bodySHA`. Job re-fetches full
body from GitHub when needed.

## Phase machine (`status.phase`)

```
Triage → PendingValidation → Ready → Executing → PROpen → Done
                |                 |
                v                 v
            Rejected           Failed
```

| Phase | Operator action |
|---|---|
| `Triage` | Ensure Job 1 exists; wait |
| `PendingValidation` | Job 1 done in-scope; requeue while `!spec.approved` |
| `Ready` | `spec.approved`; ensure Job 2 exists |
| `Executing` | Job 2 running; wait |
| `PROpen` | Job 2 succeeded; set `status.execution.prURL` |
| `Rejected` | Out of scope / suspicious / blacklisted |
| `Failed` | Job failed or invalid result |
| `Done` | Optional terminal after human merge |

`Ready` is entered when the webhook sets `spec.approved=true` (operator may also
treat `approved && PendingValidation` → transition to `Ready` then spawn Job 2).

## Build roadmap (this repo)

1. **Skeleton** — Kubebuilder init, CRD types, stub reconciler — done
2. **KinD POC (current)** — manual CR → Job triage → Gemini API → logs + `status.triage`
   - Helm chart: `charts/hal-k8s-operator`
   - Runbook: [`POC.md`](POC.md) · commands: `task` ([`Taskfile.yml`](Taskfile.yml))
   - Secret `gemini-api` (not Vault yet)
3. **HITL approve** — `spec.approved` (kubectl patch for now)
4. **Webhook receiver** — create CR + `"agent go"` admission
5. **Job 2 fix** — Sysbox + PR
6. **Vault K8s auth** — replace plain Secret
7. **Dashboard** — nice-to-have, later

## Conventions

- Language: **Go**, `sigs.k8s.io/controller-runtime`
- Scaffolding: **Kubebuilder**
- Module path: `github.com/hashicorp-academy/hal-k8s-operator` (adjust if forked)
- API group: `agent.hal.dev`, version `v1alpha1`
- Branch naming when contributing back to `hal`: `feature/**`, `bugfix/**`

## Out of scope (operator binary)

- Calling GitHub or Vault APIs  
- Running model inference inside the controller process  
- Auto-merge of PRs  
- Multipass / nested KVM validation (stays `human-only` in HAL)
