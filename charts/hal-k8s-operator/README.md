# hal-k8s-operator Helm chart

## Install

**KinD / local POC** (default `values.yaml`):

```bash
helm upgrade --install hal-k8s-operator ./charts/hal-k8s-operator \
  --namespace hal-k8s-operator-system --create-namespace \
  --set gemini.apiKey="$GEMINI_API_KEY" \
  --set github.token="$GITHUB_TOKEN"
```

When `createSecret: true` and keys are set via `--set`, Helm creates Secrets
`gemini-api`, `github-triage`, and `github-fix` (KinD may populate both GitHub
Secrets from the same local PAT; key `GITHUB_TOKEN`). Job SAs `hal-job-triage` /
`hal-job-fix` are chart-created locally (empty rights, `automount` false on
Job pods).

**GKE / GHCR OCI** (`values-ghcr.yaml`):

```bash
helm upgrade --install hal-k8s-operator \
  oci://ghcr.io/hashimiche/charts/hal-k8s-operator \
  --version 0.0.3 \
  --namespace hal-k8s-operator-system --create-namespace \
  -f charts/hal-k8s-operator/values-ghcr.yaml
```

Published chart `0.0.3` includes Job egress NetworkPolicy (`networkPolicy.enabled: true`
in the overlay) and VSO-only secrets (`createSecret: false`).

Do **not** pass `--set gemini.apiKey` or `--set github.token`. The overlay sets
`gemini.createSecret: false` and `github.createSecret: false`. Provision Secrets
with VSO (three VaultAuth channels in `hal-agent-infra`):

| Secret          | VaultAuth              | Source (Vault)           | Keys used by Jobs |
|-----------------|------------------------|--------------------------|-------------------|
| `gemini-api`    | `vault-auth-gemini`    | KV `hal-agent/llm`       | `GEMINI_API_KEY`  |
| `github-triage` | `vault-auth-triage`    | `github/token/triage`    | `GITHUB_TOKEN`    |
| `github-fix`    | `vault-auth-fix`       | `github/token/fix`       | `GITHUB_TOKEN`    |

SAs `hal-agent-vso`, `hal-job-triage`, `hal-job-fix` are created by infra on
GKE (`create: false` in the chart overlay). Operator Deployment args
(`--gemini-secret-name`, `--github-triage-secret-name`,
`--github-fix-secret-name`, etc.) default to the names above; override in
values only if VSO destination names differ.

**Removed:** `github-pat` (replaced by `github-triage` + `github-fix`).
