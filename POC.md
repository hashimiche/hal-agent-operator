# POC — step by step (vanilla machine)

The POC goal, in 3 sentences:

1. **You provide a CR** (`IssueResolution`) to the operator.
2. **The operator creates a Job** that analyzes the issue through the **Gemini** API (AI Studio key).
3. **The analysis shows up in the Job logs** (and is summarized in `status.triage`).

Every step ends with a **✅ Check** block: do not move on until it passes.

---

## Step 0 — Prerequisites (once)

You need: a container engine (**docker** or **podman**), **kind**, **kubectl**, **helm**, **go**, **task**, and a **Gemini API key**.

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

### 0.3 The Gemini API key

Go to [aistudio.google.com/apikey](https://aistudio.google.com/apikey) → **Create API key**.
The **free tier** is enough for this POC (no billing to enable — unlike the
Anthropic API, which is not included in a Claude Pro subscription).

```bash
export GEMINI_API_KEY='AIza...'   # never commit it
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

## Step 2 — Build the image + load it into KinD

```bash
task kind-poc-image
```

**✅ Check**: the command finishes without error and prints
`Image ... loaded` (docker) or the archive load (podman).

---

## Step 3 — Deploy the operator (Helm) + Gemini Secret

```bash
task kind-poc-helm    # refuses to run if GEMINI_API_KEY is empty
```

This installs: the `issueresolutions.agent.hal.dev` CRD, the operator
Deployment, the RBAC, and a `gemini-api` Secret holding your key.

**✅ Check**:

```bash
kubectl -n hal-agent get deploy,pods,secret
# deploy hal-agent-hal-k8s-operator: READY 1/1
# secret gemini-api: present
kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator | tail -5
# must show "Starting manager" with no error
```

---

## Step 4 — Provide a CR (this is YOUR demo action)

```bash
kubectl apply -f config/samples/agent_v1alpha1_issueresolution.yaml
```

(or your own issue — edit `title` / `body` in that file)

**✅ Check**:

```bash
kubectl -n hal-agent get issueresolutions
# NAME         PHASE     APPROVED   ISSUE   AUTHOR
# issue-1234   Triage    false      1234    alice
kubectl -n hal-agent get jobs
# issue-1234-triage created
```

---

## Step 5 — Read the Gemini analysis (the expected result)

```bash
kubectl -n hal-agent logs job/issue-1234-triage -f
```

You should see: the issue body → `--- calling Gemini ---` → the raw response
→ the final JSON `{"inScope": ..., "suspicious": ..., "summary": ...}`.

The summary also lands in the CR (~10 s after the Job completes):

```bash
kubectl -n hal-agent get issueresolution issue-1234 -o jsonpath='{.status.triage}' | jq
kubectl -n hal-agent get issueresolutions
# PHASE = PendingValidation (in scope) or Rejected (out of scope)
```

**✅ Check — POC success criterion**: the Job logs contain the Gemini
analysis, and `status.triage.summary` is filled in.

---

## Step 6 (bonus) — Simulate the human approval

```bash
kubectl -n hal-agent patch issueresolution issue-1234 --type merge \
  -p '{"spec":{"approved":true}}'
kubectl -n hal-agent get issueresolutions
# PHASE moves to Ready ("Approved — POC ends here")
```

---

## Replay / Clean up

```bash
# Replay with another issue: change metadata.name (e.g. issue-1235) and apply
kubectl -n hal-agent delete issueresolution issue-1234   # also deletes the Job (ownerRef)

# Tear everything down (helm uninstall + kind delete cluster)
task kind-poc-clean
```

---

## Quick troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| `helm … namespaces "hal-agent" already exists` | Old chart also created a `Namespace` object | Chart fixed — re-run `task kind-poc-helm`; if release is failed: `helm uninstall hal-agent -n hal-agent` then retry |
| Operator pod in `ImagePullBackOff` / `ErrImageNeverPull` | Image not loaded into KinD (or unqualified name under podman) | Redo step 2 (`task kind-poc-image` handles the `docker.io/library/` prefix) |
| `podman build`: `stat .../cmd/main.go: directory not found` | Overly aggressive `.dockerignore` (old Kubebuilder scaffold) | The repo ships a fixed `.dockerignore` — `git pull` / rebuild |
| Job in `CreateContainerConfigError` | `gemini-api` Secret missing | `kubectl -n hal-agent get secret gemini-api`; redo step 3 with the key exported |
| Job logs: `HTTP 400/403 API key not valid` | Invalid Gemini key | Regenerate on aistudio.google.com/apikey, redo step 3 |
| Job logs: `Error 404 … model … no longer available to new users` | Pinned model ID retired for new API keys (happened to `gemini-2.5-flash` on 2026-07-09) | Default is now the rolling alias `gemini-flash-latest`; redeploy (`task kind-poc-helm`), then delete + re-apply the CR |
| Job logs: `HTTP 429 RESOURCE_EXHAUSTED` | Free-tier quota hit | Wait 1 min and re-apply the CR; if recurrent, try another model tier: `task kind-poc-helm -- --set triage.model=<model>` (list: `curl -s -H "x-goog-api-key: $GEMINI_API_KEY" https://generativelanguage.googleapis.com/v1beta/models \| jq -r '.models[].name'`) |
| `PHASE` stays `Triage` with no Job | Operator not Ready | `kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator` |
| `kind create cluster` fails under podman/WSL2 | Finicky podman provider | `podman machine` is not needed under WSL2; check `systemctl --user status podman.socket`, otherwise install docker.io (fallback) |
