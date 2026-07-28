# POC — step by step (vanilla machine)

The POC goal, in 3 sentences:

1. **You provide a CR** (`IssueResolution`) to the operator.
2. **Job 1 (triage)** analyzes the issue via **Gemini** and posts feedback on GitHub.
3. **After approval, Job 2 (fix)** clones the fixture branch, runs `go test`, applies a fix, and **opens a PR** on the target fork.

Every step ends with a **✅ Check** block: do not move on until it passes.

**Deep dive:** [docs/plans/T11-chart-runbook-job2-kind.md](docs/plans/T11-chart-runbook-job2-kind.md) (chart values, per-bug table, troubleshooting). Fixture bugs: [docs/plans/T8-fixture-fork-bugs.md](docs/plans/T8-fixture-fork-bugs.md) §6.

---

## Step 0 — Prerequisites (once)

You need: a container engine (**docker** or **podman**), **kind**, **kubectl**, **helm**, **go**, **task**, a **Gemini API key**, and a **GitHub PAT** scoped to the fixture fork.

### 0.1 Install `task` (go-task)

```bash
go install github.com/go-task/task/v3/cmd/task@latest
export PATH="$PATH:$(go env GOPATH)/bin"   # add to your ~/.bashrc
task --version
```

### 0.2 Install whatever is missing (Ubuntu / WSL2)

**Container engine** — podman is already present on this machine, nothing to do.
(Otherwise: `sudo apt-get install -y docker.io` then `sudo usermod -aG docker $USER`.)

**kind**:

```bash
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.30.0/kind-linux-amd64
chmod +x ./kind && sudo mv ./kind /usr/local/bin/kind
```

**helm**:

```bash
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
```

**kubectl** and **go**: already installed here (`kubectl version --client`, `go version` ≥ 1.24).

### 0.3 API keys (never commit)

**Gemini** — [aistudio.google.com/apikey](https://aistudio.google.com/apikey) → **Create API key**.
The free tier is enough for this POC.

**GitHub PAT** — fine-grained token on repo **`hashimiche/test-hal-operator`** only:

| Scope | Used by |
|---|---|
| `issues:write` | Job 1 — triage comment + labels |
| `contents:write` | Job 2 — clone, commit, push `bugfix/**` branch |
| `pull_requests:write` | Job 2 — open PR against `fixture/bugN` |

Never grant merge or admin.

```bash
export GEMINI_API_KEY='AIza...'   # never commit
export GITHUB_TOKEN='ghp_...'     # never commit
```

### 0.4 Global check

```bash
cd ~/git/hashicorp_academy_labs/hal-k8s-operator
task prereqs
```

> Podman is handled automatically by the Taskfile (KinD provider + build):
> no variable to export.

**✅ Check**: `task prereqs` prints `OK` on every line and exits without error.

---

## Step 1 — KinD cluster

```bash
task kind-poc-cluster
```

**✅ Check**:

```bash
kubectl cluster-info --context kind-hal-agent
kubectl get nodes    # 1 node "hal-agent-control-plane" in Ready
```

---

## Step 2 — Build images + load into KinD

Builds **two** images: operator (distroless: `/manager`, `/triage`) and fix (Go toolchain: `/fix`).

```bash
task kind-poc-image
```

**✅ Check**: the command finishes without error and prints
`Image ... loaded` (docker) or the archive load (podman).

---

## Step 3 — Deploy the operator (Helm) + Secrets

```bash
task kind-poc-helm    # requires GEMINI_API_KEY + GITHUB_TOKEN
```

This installs: CRD, operator Deployment, RBAC, `gemini-api` Secret, `github-pat` Secret,
and wires `fix.image` for Job 2.

**✅ Check**:

```bash
kubectl -n hal-agent get deploy,pods,secret
# deploy hal-agent-hal-k8s-operator: READY 1/1
# secret gemini-api: present
# secret github-pat: present
kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator | tail -5
# must show "Starting manager" with no error
```

Or run the full bootstrap in one shot: `task kind-poc` (steps 1–3).

---

## Step 4 — Provide a CR (fixture issue #5)

Prefer the Job 2 samples (pre-approved — triage then fix Job without a patch):

```bash
kubectl apply -f config/samples/job2/issue-5.yaml
```

Key fields:

```yaml
metadata:
  name: issue-5
spec:
  repository: hashimiche/test-hal-operator
  issueNumber: 5
  baseBranch: fixture/bug1   # Job 2 clone + PR base (required for fixture bugs)
  approved: true             # POC shortcut; production uses create-cr + "agent go"
  approvedBy: hashimiche
```

For other fixture bugs: `config/samples/job2/issue-{6,7,8}.yaml` (see table below).

To demo the human gate alone, apply `config/samples/agent_v1alpha1_issueresolution.yaml`
(`approved: false`) and use Step 6.

**✅ Check**:

```bash
kubectl -n hal-agent get issueresolutions
# NAME       PHASE     APPROVED   ISSUE   AUTHOR
# issue-5    Triage    true       5       hashimiche
kubectl -n hal-agent get jobs
# issue-5-triage created
```

---

## Step 5 — Triage (Job 1): Gemini analysis + GitHub feedback

```bash
kubectl -n hal-agent logs job/issue-5-triage -f
```

You should see: the issue body → `--- calling Gemini ---` → the raw response
→ the final JSON → `--- posting GitHub feedback ---` → comment URL.

The Job posts a markdown comment on GitHub issue #5 and applies labels
(`triage:executed`, `suspicious:true|false`, `in-scope:true|false`,
`agent:pending-validation` or `agent:rejected`).

```bash
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.triage}' | jq
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.plan}' | jq
# plan.commentURL = GitHub comment HTML URL
kubectl -n hal-agent get issueresolutions
# PHASE = PendingValidation (in scope) or Rejected (out of scope)
```

**✅ Check — triage success**: Job logs contain the Gemini analysis,
`status.triage.summary` is filled, and issue #5 has the triage comment + labels.

---

## Step 6 — Approve (human gate #1)

**Skip** if you used `config/samples/job2/*` (`approved: true` already) — after
triage the controller moves `PendingValidation` → `Ready` → `Executing` on its own.

Otherwise (sample with `approved: false`), simulate `"agent go"`:

```bash
kubectl -n hal-agent patch issueresolution issue-5 --type merge \
  -p '{"spec":{"approved":true,"approvedBy":"hashimiche"}}'
kubectl -n hal-agent get issueresolutions -w
# PHASE: Ready (brief) → Executing
```

**✅ Check**: phase moves to **`Executing`** (not terminal). A fix Job appears:

```bash
kubectl -n hal-agent get jobs
# issue-5-fix-1
```

---

## Step 7 — Fix (Job 2): clone, test, PR

```bash
kubectl -n hal-agent logs job/issue-5-fix-1 -f
```

Expect: clone **`fixture/bug1`** → baseline `go test` **FAIL** → Gemini locate/fix →
retest → push `bugfix/**` → open PR (base = **`fixture/bug1`**) → JSON result on stdout.

**✅ Check — Job 2 running**: logs show clone + `go test` output (RED on baseline for bug #5).

---

## Step 8 — Verify PR open (`PROpen`)

Wait until the fix Job completes and the controller reads the termination-log.

```bash
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.phase}{"\n"}'
# PROpen
kubectl -n hal-agent get issueresolution issue-5 -o jsonpath='{.status.execution}' | jq
# attempt, jobName, branch, prURL, prNumber
```

Open `status.execution.prURL` in a browser. Confirm:

- PR **base** = `fixture/bug1` (not `main`)
- PR **head** = `bugfix/**`
- Body references issue #5

**✅ Check — full POC success**: phase **`PROpen`**, `status.execution.prURL` is valid,
PR targets the correct fixture branch. Human merge is optional (gate #2, out of operator scope).

If phase is **`Failed`**, see troubleshooting below (common for multi-file bug #8).

---

## Fixture bugs — per-issue reference

Repo: **`hashimiche/test-hal-operator`**. Always set `spec.baseBranch` to the matching fixture branch.

| CR `metadata.name` | Issue | `spec.baseBranch` | RED test (local repro) |
|---|---|---|---|
| `issue-5` | 5 | `fixture/bug1` | `go test ./cmd/mcp/ -run TestIsAllowedOrigin` |
| `issue-6` | 6 | `fixture/bug2` | `go test ./internal/global/ -run TestVaultOIDCProbesAuthentik` |
| `issue-7` | 7 | `fixture/bug3` | `go test ./cmd/boundary/ -run TestSharedVaultMariaDBEndpoint` |
| `issue-8` | 8 | `fixture/bug4` | `go test ./cmd/mcp/ -run 'TestVaultPkiRecommendationsSurfaced\|TestValidateCommandReflectsCurrentSurface'` |

Target files and symptom-only issue text: [T8 §6](docs/plans/T8-fixture-fork-bugs.md#6-documentation-de-la-fixture-section-pour-le-runbook-job-2).

---

## Replay / Clean up

```bash
# Another bug: delete CR, edit sample (name, issueNumber, baseBranch, title/body), re-apply
kubectl -n hal-agent delete issueresolution issue-5

# Tear everything down (helm uninstall + kind delete cluster)
task kind-poc-clean
```

---

## Quick troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| `helm … namespaces "hal-agent" already exists` | Old chart also created a `Namespace` object | Chart fixed — re-run `task kind-poc-helm`; if release is failed: `helm uninstall hal-agent -n hal-agent` then retry |
| Operator pod in `ImagePullBackOff` / `ErrImageNeverPull` | Image not loaded into KinD (or unqualified name under podman) | Redo step 2 (`task kind-poc-image` handles the `docker.io/library/` prefix) |
| Fix Job in `ImagePullBackOff` | Fix image not built/loaded | Redo `task kind-poc-image`; chart `fix.image` must match loaded tag |
| `podman build`: `stat .../cmd/main.go: directory not found` | Overly aggressive `.dockerignore` (old Kubebuilder scaffold) | The repo ships a fixed `.dockerignore` — `git pull` / rebuild |
| Job in `CreateContainerConfigError` | `gemini-api` or `github-pat` Secret missing | `kubectl -n hal-agent get secret`; redo Helm with keys exported |
| Job logs: `HTTP 400/403 API key not valid` | Invalid Gemini key | Regenerate on aistudio.google.com/apikey, redo step 3 |
| Job logs: `HTTP 429 RESOURCE_EXHAUSTED` | Free-tier quota hit | Wait 1 min and re-apply the CR; override model: `task kind-poc-helm -- --set triage.model=<model>` |
| `PHASE` stays `Triage` with no Job | Operator not Ready | `kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator` |
| Approve → stays `Ready`, no fix Job | T9 controller not deployed | Operator logs; ensure T9/T10 code is in the running image |
| Fix Job: baseline tests **pass** (unexpected) | Cloned wrong branch (`main` not `fixture/bugN`) | Set `spec.baseBranch`; verify Job env `BASE_BRANCH` |
| Fix Job: `403` on push or PR | PAT missing `contents:write` / `pull_requests:write` | Regenerate PAT with all three scopes (step 0.3) |
| `PHASE` = `Failed`, `FixAttemptsExhausted` | Fix did not pass retest (expected on issue #8) | Read fix Job logs; see [T11 troubleshooting](docs/plans/T11-chart-runbook-job2-kind.md#7-troubleshooting) |
| PR base is `main` instead of `fixture/bugN` | `baseBranch` not set or not wired | CR must include `spec.baseBranch`; see T11 plan |
| `dial tcp [2607:…]:443: network is unreachable` (Go mods / Gemini) | KinD/Podman IPv6 egress broken | `task kind-poc-dns` then retry Job; new clusters use `config/kind/hal-agent.yaml` (IPv4-only) |
| `kind create cluster` fails under podman/WSL2 | Finicky podman provider | `podman machine` is not needed under WSL2; check `systemctl --user status podman.socket`, otherwise install docker.io (fallback) |

---

## Optional — validate a GHCR release (T12)

After you push a `v*` tag and the **Publish** workflow succeeds, smoke-test the OCI chart on KinD (or any cluster that can pull from GHCR):

```bash
# Replace 0.2.0 / v0.2.0 with your tag (chart version = tag without leading v).
export RELEASE=v0.2.0
export CHART_VER="${RELEASE#v}"

kind create cluster --name hal-agent-ghcr --config config/kind/hal-agent.yaml

helm install hal-agent oci://ghcr.io/hashimiche/charts/hal-k8s-operator \
  --version "$CHART_VER" \
  --namespace hal-agent --create-namespace \
  -f charts/hal-k8s-operator/values-ghcr.yaml \
  --set gemini.apiKey="$GEMINI_API_KEY" \
  --set github.token="$GITHUB_TOKEN"

kubectl -n hal-agent wait --for=condition=available deployment/hal-agent-hal-k8s-operator --timeout=120s
```

Then continue from **Step 4** (apply a sample CR). Full install reference: [README § Helm chart (OCI on GHCR)](README.md#helm-chart-oci-on-ghcr).

**Blockers without a real tag push:** local `helm package` / `helm template` only; OCI `helm install` needs a published chart and pullable GHCR images.
