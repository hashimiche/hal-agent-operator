# POC local (KinD) — triage Claude

But : tu crées une CR à la main → l’opérateur lance un **Job** → le Job appelle
**Claude** avec le contenu de l’issue → le résultat est dans les **logs du Job**
(et un résumé dans `status.triage`).

Pas de webhook GitHub, pas de Vault, pas de Job 2. Clé API = Secret Kubernetes.

## Prérequis

- `kind`, `kubectl`, `helm`, `docker` (ou compatible)
- Une clé API Anthropic (`ANTHROPIC_API_KEY`) — console Anthropic (Claude API)

> Claude Pro (chat) ≠ forcément une clé API. Il faut une clé sur
> [console.anthropic.com](https://console.anthropic.com/).

## 1. Cluster KinD

```bash
kind create cluster --name hal-agent
```

## 2. Build + load image

```bash
cd hal-k8s-operator
make docker-build IMG=hal-k8s-operator:poc
kind load docker-image hal-k8s-operator:poc --name hal-agent
```

## 3. Install via Helm

```bash
export ANTHROPIC_API_KEY='sk-ant-...'   # ta clé — ne la commit jamais

helm upgrade --install hal-agent ./charts/hal-k8s-operator \
  --namespace hal-agent \
  --create-namespace \
  --set image.repository=hal-k8s-operator \
  --set image.tag=poc \
  --set image.pullPolicy=IfNotPresent \
  --set claude.apiKey="$ANTHROPIC_API_KEY"
```

Vérifier :

```bash
kubectl -n hal-agent get deploy,pods,secret
kubectl -n hal-agent logs deploy/hal-agent-hal-k8s-operator -f
```

## 4. Créer une CR manuelle

```bash
kubectl apply -f - <<'EOF'
apiVersion: agent.hal.dev/v1alpha1
kind: IssueResolution
metadata:
  name: issue-1234
  namespace: hal-agent
spec:
  repository: hashimiche/hal
  issueNumber: 1234
  author: alice
  title: "docs: typo in vault oidc skill"
  body: |
    The OIDC skill examples still mention an old flag name.
    Please fix the wording and keep EXAMPLES.md in sync.
  approved: false
EOF
```

## 5. Voir le résultat Claude

```bash
kubectl -n hal-agent get issueresolutions -w
kubectl -n hal-agent get jobs
kubectl -n hal-agent logs job/issue-1234-triage
kubectl -n hal-agent get issueresolution issue-1234 -o yaml
```

Tu dois voir dans les logs du Job : body de l’issue, réponse brute Claude, JSON
`inScope` / `suspicious` / `summary`. Le même résumé arrive dans `status.triage`.

## Nettoyage

```bash
helm uninstall hal-agent -n hal-agent
kind delete cluster --name hal-agent
```
