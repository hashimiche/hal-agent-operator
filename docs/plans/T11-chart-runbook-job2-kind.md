# Plan T11 — Chart + KinD runbook for Job 2 (fix → PR)

> **Audience:** operators and agents running the KinD POC end-to-end.
> **Depends on:** T8 fixture fork, T9/T10 controller + `cmd/fix`, Job 1 GitHub comment/labels (KinD-validated 2026-07-27).
> **Companion docs:** [`POC.md`](../../POC.md) (step-by-step), [`T8-fixture-fork-bugs.md`](T8-fixture-fork-bugs.md) §6 (fixture mapping), [`T9-T10-job2-fixer-architecture.md`](T9-T10-job2-fixer-architecture.md) (architecture).

---

## 1. Goal & acceptance

**Goal:** On a local KinD cluster, a human applies an `IssueResolution` CR for a fixture issue on `hashimiche/test-hal-operator`, runs triage (Job 1), approves, and Job 2 clones the correct fixture branch, runs `go test`, applies a Gemini fix, and opens a PR against that branch.

**Acceptance (T11 done when all pass):**

1. `task kind-poc` succeeds (cluster + operator + fix images + Helm).
2. Apply sample CR `issue-5` → triage completes → `PendingValidation` + GitHub comment/labels on issue #5.
3. `kubectl patch … spec.approved=true` → phase `Ready` → `Executing` → fix Job `issue-5-fix-1` runs.
4. Fix Job clones **`fixture/bug1`** (not `main`), sees RED test, opens PR with base **`fixture/bug1`** and head `bugfix/**`.
5. CR reaches `PROpen`; `status.execution.prURL` is a valid GitHub PR URL.
6. Entire flow matches [`POC.md`](../../POC.md) with no ad-hoc deviations.

Human merge of the PR is **out of scope** (gate #2); `PROpen` is terminal for the operator POC.

---

## 2. Prerequisites

| Requirement | Notes |
|---|---|
| T9/T10 code landed | Controller phases `Ready`→`Executing`→`PROpen`; `cmd/fix` + separate fix image. Commit if still local-only. |
| T11 API field | `spec.baseBranch` (optional string). When set, Job 2 env `BASE_BRANCH` and PR base = that branch. |
| T8 fixture fork | Repo `hashimiche/test-hal-operator`; branches `fixture/bug1`–`fixture/bug4`; issues #5–#8 open with `bug` label. |
| Local tools | docker or podman, kind, kubectl, helm, go, task — see POC.md step 0. |
| `GEMINI_API_KEY` | Google AI Studio; free tier OK. |
| `GITHUB_TOKEN` | Fine-grained PAT on **`hashimiche/test-hal-operator` only**. Scopes: **`issues:write`** (Job 1) + **`contents:write`** + **`pull_requests:write`** (Job 2). Never merge/admin. |
| Network | Fix Job clones GitHub and calls Gemini; KinD node needs egress. |

Verify fixture branches exist:

```bash
gh api repos/hashimiche/test-hal-operator/branches/fixture/bug1 --jq .name
# repeat for bug2, bug3, bug4
```

---

## 3. Chart values checklist

KinD deploy via `task kind-poc-helm` (wraps Helm with sane POC defaults). Confirm or override:

| Value | Purpose | KinD default |
|---|---|---|
| `fix.image` | Fix Job container (Go toolchain + `/fix`) | `hal-k8s-operator-fix:poc` (loaded by `task kind-poc-image`) |
| `github.token` → Secret `github-pat` | `GITHUB_TOKEN` in triage + fix Jobs | `--set github.token=$GITHUB_TOKEN` |
| `github.secretName` / `github.secretKey` | Secret mount | `github-pat` / `GITHUB_TOKEN` |
| `gemini.apiKey` → Secret `gemini-api` | `GEMINI_API_KEY` in both Jobs | `--set gemini.apiKey=$GEMINI_API_KEY` |
| `runtimeClassName` | Job pod runtime class; empty = cluster default | `""` (explicit empty default — no Sysbox/nested containers needed) |
| `triage.model` | Gemini model for both Jobs | chart default; override if quota errors |

**Images:** `task kind-poc-image` builds two targets — `operator` (distroless: `/manager`, `/triage`) and `fix` (golang: `/fix`). Both must be loaded into KinD before Helm install.

**Secrets:** Never commit tokens. POC uses plain K8s Secrets; Vault replaces this at T15.

---

## 4. API: `spec.baseBranch`

```yaml
spec:
  repository: hashimiche/test-hal-operator
  issueNumber: 5
  baseBranch: fixture/bug1   # optional; omit → clone/PR use repo default branch (main)
```

| Field | Controller / Job behavior |
|---|---|
| `spec.baseBranch` set | Fix Job env `BASE_BRANCH=<value>`; `cmd/fix` clones that ref; PR **base** = that branch. |
| `spec.baseBranch` omitted | Clone repo default branch (`main` on the fork); PR base = default. **Wrong for T8 bugs** — always set for fixture issues. |

Mapping issue → branch (T8):

| Issue | `spec.baseBranch` |
|---|---|
| #5 | `fixture/bug1` |
| #6 | `fixture/bug2` |
| #7 | `fixture/bug3` |
| #8 | `fixture/bug4` |

---

## 5. KinD procedure (step-by-step)

### 5.1 Bootstrap

```bash
cd hal-k8s-operator
export GEMINI_API_KEY='…'    # never commit
export GITHUB_TOKEN='ghp_…'  # never commit; fork-scoped PAT (see §2)

task kind-poc    # cluster + images + helm
```

**Check:** `kubectl -n hal-agent get deploy,pods,secret` — operator Ready, `gemini-api` + `github-pat` present.

### 5.2 Apply CR (fixture issue #5)

```bash
kubectl apply -f config/samples/agent_v1alpha1_issueresolution.yaml
```

Sample uses `metadata.name: issue-5`, `issueNumber: 5`, `baseBranch: fixture/bug1`.

**Check:**

```bash
kubectl -n hal-agent get issueresolution issue-5
# PHASE=Triage → Job issue-5-triage created
```

### 5.3 Triage (Job 1)

```bash
kubectl -n hal-agent logs job/issue-5-triage -f
```

Expect: Gemini analysis → `--- posting GitHub feedback ---` → comment URL.

```bash
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.phase}{"\n"}'
# PendingValidation (in scope) or Rejected
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.triage.summary}' | head -c 200
```

**Check on GitHub:** issue #5 has triage comment + labels (`triage:executed`, `suspicious:*`, `in-scope:*`, `agent:pending-validation` or `agent:rejected`).

### 5.4 Approve

```bash
kubectl -n hal-agent patch issueresolution issue-5 --type merge \
  -p '{"spec":{"approved":true}}'
```

**Check:**

```bash
kubectl -n hal-agent get issueresolution issue-5
# PHASE: Ready (brief) → Executing
kubectl -n hal-agent get jobs
# issue-5-fix-1 created
```

### 5.5 Fix (Job 2)

```bash
kubectl -n hal-agent logs job/issue-5-fix-1 -f
```

Expect (order may vary slightly): clone `fixture/bug1` → baseline `go test` FAIL → Gemini locate/fix → retest → commit/push → PR created → termination-log JSON on stdout.

**Check CR:**

```bash
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.phase}{"\n"}'
# PROpen
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.execution}' | jq
# prURL, prNumber, branch, attempt, jobName
```

**Check GitHub:** open `status.execution.prURL` — PR base = **`fixture/bug1`**, head = `bugfix/**`, targets issue #5.

### 5.6 Replay other bugs

Delete CR (cascades Jobs via ownerRef), edit sample or duplicate CR:

| CR name | Issue | `baseBranch` |
|---|---|---|
| `issue-5` | 5 | `fixture/bug1` |
| `issue-6` | 6 | `fixture/bug2` |
| `issue-7` | 7 | `fixture/bug3` |
| `issue-8` | 8 | `fixture/bug4` |

```bash
kubectl -n hal-agent delete issueresolution issue-5
# edit fields, re-apply
```

### 5.7 Teardown

```bash
task kind-poc-clean
```

---

## 6. Per-bug fixture table (issues #5–#8)

From [T8 §6](T8-fixture-fork-bugs.md#6-documentation-de-la-fixture-section-pour-le-runbook-job-2).

| Issue | `baseBranch` | Target file(s) | RED test command |
|---|---|---|---|
| **#5** | `fixture/bug1` | `cmd/mcp/mcp.go` → `isAllowedOrigin` | `go test ./cmd/mcp/ -run TestIsAllowedOrigin` |
| **#6** | `fixture/bug2` | `internal/global/status_snapshot.go` → OIDC probe (+ seam `internal/global/global.go`) | `go test ./internal/global/ -run TestVaultOIDCProbesAuthentik` |
| **#7** | `fixture/bug3` | `cmd/boundary/defaults.go` → `vaultMariaDBContainer` mirror | `go test ./cmd/boundary/ -run TestSharedVaultMariaDBEndpoint` |
| **#8** | `fixture/bug4` | `cmd/mcp/advanced.go` + `cmd/mcp/ops_api.go` (multi-file — Job 2 mono-file limit) | `go test ./cmd/mcp/ -run 'TestVaultPkiRecommendationsSurfaced\|TestValidateCommandReflectsCurrentSurface'` |

**Expected Job 2 outcomes:**

- **#5, #6, #7:** mono-file fix → PR may pass CI on `bugfix/**` after human review.
- **#8:** demonstrates multi-file bug; fix Job may fail or open PR that stays RED — acceptable POC outcome per T8 design.

Local RED repro (outside cluster):

```bash
git clone git@github.com:hashimiche/test-hal-operator.git && cd test-hal-operator
git switch fixture/bug1 && go test ./cmd/mcp/ -run TestIsAllowedOrigin   # FAIL
```

---

## 7. Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| Phase stuck `Ready`, no fix Job | T9 not deployed or reconciler error | Operator logs: `kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator` |
| Fix Job `ImagePullBackOff` | Fix image not loaded into KinD | Re-run `task kind-poc-image`; confirm `fix.image` matches loaded tag |
| Fix logs: clone OK but tests pass on baseline | Wrong branch — cloned `main` not `fixture/bugN` | Set `spec.baseBranch: fixture/bug<N>`; confirm `BASE_BRANCH` in Job env |
| Fix logs: `403` / `404` on clone or push | PAT scope or wrong repo | PAT on fork only; scopes `contents:write` + `pull_requests:write`; re-run `task kind-poc-helm` |
| Fix logs: `go test` / module download errors | Job pod no egress or slow network | Check pod events; fix Job has `ActiveDeadlineSeconds` (~600s) |
| `dial tcp [2607:…]:443: network is unreachable` (Go or Gemini) | KinD/Podman IPv6 broken; client used AAAA | `task kind-poc-dns` (CoreDNS: IPv4 forward + hide AAAA); recreate cluster with `config/kind/hal-agent.yaml` |
| Phase `Executing` forever | Job still running or stuck | `kubectl -n hal-agent describe job issue-N-fix-1`; watch logs |
| Phase `Failed`, `FixAttemptsExhausted` | LLM fix did not pass retest (common on #8) | Read fix Job logs + termination-log; increase `spec.maxFixAttempts` for retry experiments |
| Phase `Failed` after Job `Succeeded` | Termination-log unreadable or missing `prURL` | Describe pod; check succeeded container `ExitCode==0` and message JSON |
| PR opened but base is `main` | `baseBranch` not wired or CR field omitted | Verify T11 code: `spec.baseBranch` → `BASE_BRANCH` → go-github PR base |
| Job 1 comment OK, Job 2 auth fail | PAT missing write scopes | Regenerate PAT with all three scopes (§2) |
| `CreateContainerConfigError` on fix Job | Missing Secret | `kubectl -n hal-agent get secret gemini-api github-pat` |

---

## 8. Phase reference

```
Triage → PendingValidation → Ready → Executing → PROpen
                                  ↘ Failed (maxFixAttempts exhausted)
PendingValidation → Rejected (out of scope / suspicious)
```

Job names: `issue-<n>-triage`, `issue-<n>-fix-<attempt>` (e.g. `issue-5-fix-1`).

---

## 9. Out of scope (T11)

- **T12+** — GHCR/OCI chart publish, CI for operator images.
- **T13–T14** — GKE deploy, Terraform, WIF.
- **T15** — Vault dynamic secrets (replace `gemini-api` / `github-pat`).
- **T16** — `create-cr` workflow, `"agent go"` on production `hal` repo (KinD uses manual CR + patch).
- Auto-merge of fix PRs.
- Sysbox / nested Docker in Jobs (`runtimeClassName` stays empty).
- Changing fixture bugs or issues (T8 complete).

---

## 10. Files touched by T11 (operator repo)

| File | Role |
|---|---|
| [`POC.md`](../../POC.md) | User-facing KinD steps 0–8 (triage + Job 2) |
| [`config/samples/agent_v1alpha1_issueresolution.yaml`](../../config/samples/agent_v1alpha1_issueresolution.yaml) | KinD sample CR for issue #5 |
| [`config/samples/issueresolution.template.yaml`](../../config/samples/issueresolution.template.yaml) | GHA template; optional `baseBranch` commented |
| [`Taskfile.yml`](../../Taskfile.yml) | `kind-poc*` tasks; pointers to POC.md |
| [`charts/hal-k8s-operator/values.yaml`](../../charts/hal-k8s-operator/values.yaml) | `fix.image`, `github.*`, `runtimeClassName` |
| Code agent (parallel) | `spec.baseBranch` in API, controller env, `cmd/fix` clone/PR base |

**Validation after doc + code land:**

```bash
task lint-fix && task test
# then full KinD E2E per §1 acceptance
```
