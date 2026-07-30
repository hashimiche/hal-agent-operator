# Plan T14 — Deploy the operator on GKE

> **Audience:** operators deploying the GHCR release onto the T13 lab cluster.
> **Depends on:** T12 OCI chart + images, T13 `hal-agent-infra` apply (GKE +
> `hal-agent` ns), T11 KinD flow as the acceptance template.
> **Companion:** [`../hal-agent-infra/README.md`](../../../hal-agent-infra/README.md),
> [`POC.md`](../../POC.md), [`values-ghcr.yaml`](../../charts/hal-k8s-operator/values-ghcr.yaml).

---

## 1. Goal & acceptance

**Goal:** Install the published operator on the lab GKE cluster (GHCR images +
chart), with Job egress NetworkPolicy enforced, using K8s Secrets until Vault
(T15). Replay the KinD Job 2 scenario with a manual CR.

**Acceptance (T14 done when):**

1. Calico / GKE NetworkPolicy addon enabled on the lab cluster.
2. `helm upgrade --install` succeeds in `hal-agent` (CRDs + controller + Job NP).
3. Manual CR → triage → `kubectl patch approved` → fix → `PROpen` on the
   fixture fork (same path as T11 / POC.md steps 4–8).

---

## 2. Prerequisites

| Requirement | Notes |
|---|---|
| T12 artifacts | Tag `v0.0.3` (or newer): `ghcr.io/hashimiche/hal-k8s-operator(:-fix):<tag>` + `oci://ghcr.io/hashimiche/charts/hal-k8s-operator` |
| T13 lab | `hal-agent-infra/envs/lab` applied; outputs usable |
| Tools | `gcloud`, `kubectl`, `helm`, ADC (or `GOOGLE_IMPERSONATE_SERVICE_ACCOUNT`) |
| `GEMINI_API_KEY` | Google AI Studio |
| `GITHUB_TOKEN` | Fine-grained PAT on **`hashimiche/test-hal-operator`**: `issues:write` + `contents:write` + `pull_requests:write` |
| GHCR pull | Packages public, or `imagePullSecrets` in the namespace |

**NetworkPolicy chart change** (Job egress template) may be ahead of the last
published chart. Prefer install from **local chart + GHCR images** until you
retag (`v0.0.3+`) after this lands on `main`.

---

## 3. Enable NetworkPolicy on GKE (infra)

In `hal-agent-infra`, Calico is enabled via `modules/gke` (`network_policy` +
addon). Apply from the lab root (nodes recreate briefly):

```bash
cd /path/to/hal-agent-infra/envs/lab
# ensure GOOGLE_IMPERSONATE_SERVICE_ACCOUNT / ADC as for T13
terraform plan
terraform apply
```

Verify:

```bash
gcloud container clusters describe "$(terraform output -raw cluster_name)" \
  --region "$(terraform output -raw cluster_location)" \
  --project "$(terraform output -raw project_id)" \
  --format='yaml(networkPolicy,addonsConfig.networkPolicyConfig)'
```

Expect `networkPolicy.enabled: true` and addon not disabled.

**Sizing:** single-node lab needs enough CPU for Calico (`calico-typha` requests
~200m) plus Vault and the operator. Default machine type is `e2-standard-4`
(`modules/gke`). If `calico-typha` stays Pending with `Insufficient cpu`, bump
the node pool and re-apply.

---

## 4. kubeconfig

```bash
cd /path/to/hal-agent-infra/envs/lab
gcloud container clusters get-credentials "$(terraform output -raw cluster_name)" \
  --region "$(terraform output -raw cluster_location)" \
  --project "$(terraform output -raw project_id)"

kubectl get ns "$(terraform output -raw hal_agent_namespace)"
```

Use an identity that can install CRDs / ClusterRoles (lab admin / TF runner
impersonation). The GHA deployer SA is **IssueResolution-only** — not for Helm.

---

## 5. Helm install (recommended: local chart + GHCR images)

From `hal-k8s-operator` (never commit keys):

```bash
export GEMINI_API_KEY='...'   # never commit
export GITHUB_TOKEN='ghp_...' # never commit

helm upgrade --install hal-agent ./charts/hal-k8s-operator \
  --namespace hal-agent --create-namespace \
  -f charts/hal-k8s-operator/values-ghcr.yaml \
  --set image.tag=v0.0.2 \
  --set gemini.apiKey="$GEMINI_API_KEY" \
  --set github.token="$GITHUB_TOKEN"
```

**OCI-only** (Job NetworkPolicy + VSO secrets; chart `0.0.3+`):

```bash
helm upgrade --install hal-agent \
  oci://ghcr.io/hashimiche/charts/hal-k8s-operator \
  --version 0.0.3 \
  --namespace hal-agent --create-namespace \
  -f charts/hal-k8s-operator/values-ghcr.yaml
```

Do **not** pass `--set gemini.apiKey` or `--set github.token`. Provision `gemini-api` and
`github-pat` with VSO before or after install (see chart README).

(`values-ghcr.yaml` sets `networkPolicy.enabled: true` and GHCR image repos.)

Check:

```bash
kubectl -n hal-agent rollout status deploy/hal-agent-hal-k8s-operator
kubectl -n hal-agent get networkpolicy
kubectl get crd issueresolutions.agent.hal.dev
```

---

## 6. Acceptance replay (manual CR)

Same flow as KinD T11 — use a free fixture issue (e.g. #5 or #6 if still
open / re-openable):

```bash
kubectl apply -f config/samples/job2/issue-5.yaml   # adjust path / issue
# wait for PendingValidation + GitHub triage comment
kubectl -n hal-agent patch issueresolution issue-5 --type=merge \
  -p '{"spec":{"approved":true}}'
# wait for Executing → PROpen; check status.execution.prURL
```

Full step detail: [`POC.md`](../../POC.md) steps 4–8 and
[`T11-chart-runbook-job2-kind.md`](T11-chart-runbook-job2-kind.md).

---

## 7. Secrets until T15

POC still uses K8s Secrets `gemini-api` + `github-pat` created by the chart
when `createSecret` + `--set` keys are provided. Do **not** put secrets in
committed values. Vault init-container pattern is T15.

---

## 8. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| ImagePullBackOff from `ghcr.io` | Private package | Make packages public or add pull secret |
| NetworkPolicy present but Jobs hang on DNS/HTTPS | Calico not enabled, or DNS ClusterIP blocked | Re-apply infra Calico. Chart must allow UDP/TCP **53** to private CIDRs + `169.254.20.10` (Calico matches kube-dns ClusterIP pre-DNAT). Symptom: `lookup …: i/o timeout` |
| Jobs can still reach Vault SVC | NP not selected / not enforced | Confirm pod labels `hal.dev/job-role`; `kubectl describe networkpolicy` |
| Helm fails on ClusterRole / CRD | Wrong kube identity | Use admin / TF-runner impersonation, not GHA deployer SA |
| `403` on GitHub clone/PR | PAT scope / wrong repo | Fine-grained PAT on fixture fork only |

---

## 9. Out of scope here

- Vault dynamic secrets (T15)
- GitHub Actions `create-cr` / env E2E (T16 remainder)
- FQDN-only egress (Cilium) — HTTPS-to-public with private-range deny is the T14 boundary
