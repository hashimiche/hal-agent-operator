# hal-k8s-operator Helm chart

## Install

**KinD / local POC** (default `values.yaml`):

```bash
helm upgrade --install hal-k8s-operator ./charts/hal-k8s-operator \
  --namespace hal-k8s-operator-system --create-namespace \
  --set gemini.apiKey="$GEMINI_API_KEY" \
  --set github.token="$GITHUB_TOKEN"
```

Helm creates Secrets `gemini-api` and `github-pat` when `createSecret: true` and
the keys are set via `--set`.

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
with VSO before or after install:

| Secret       | Source (T15)           | Keys used by operator Jobs |
|--------------|------------------------|----------------------------|
| `gemini-api` | VaultStaticSecret (KV) | `GEMINI_API_KEY`           |
| `github-pat` | VaultDynamicSecret     | `GITHUB_TOKEN`             |

Operator Deployment args (`--gemini-secret-name`, `--github-secret-name`, etc.)
default to the names above; override in values only if VSO destination names differ.
