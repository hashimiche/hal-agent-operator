# Architecture de l'Opérateur AI K8s (Projet HAL)

> **Status:** Design · **Scope:** ce repo (`hal-k8s-operator`)  
> Complète [`hal/docs/agent-architecture.md`](../../hal/docs/agent-architecture.md) §16 (Option 4).  
> Ce doc fixe le **contrat de l'opérateur** et le workflow HITL opérationnel.

---

## 1. Modèle d'architecture globale

Approche événementielle + ressources Kubernetes éphémères, pattern Controller/Operator :

- L'opérateur maintient l'état via une Custom Resource (CR) de manière déclarative.
- L'opérateur crée des Jobs Kubernetes (Job 1 triage, Job 2 fix) et les surveille nativement via une `OwnerReference`.
- La boucle de réconciliation est déclenchée quand un Job se termine (`Succeeded` / `Failed`) — pas besoin de webhooks internes pour détecter les changements d'état des Jobs.

```mermaid
flowchart TD
    GH[GitHub issue / comment] --> WR[Webhook Receiver]
    WR -->|create / patch CR| CR[IssueResolution]
    CTRL[Operator reconcile] -.watch CR + owned Jobs.-> CR
    CTRL -->|phase needs triage| J1[Job 1: triage]
    J1 -->|OwnerRef + termination-log| CTRL
    CTRL -->|await human| WAIT[phase = PendingValidation]
    WR -->|comment "agent go" + CODEOWNERS OK| CR
    CTRL -->|phase = Ready| J2[Job 2: fix under Sysbox]
    J2 -->|OwnerRef + result| CTRL
    CTRL --> PR[phase = PROpen]
```

---

## 2. Qui fait quoi (frontière opérateur)

| Composant | Responsabilité | Dans ce repo ? |
|---|---|---|
| **Webhook Receiver** | HMAC validate ; crée la CR à l'ouverture d'issue ; sur `issue_comment` `"agent go"`, vérifie CODEOWNERS via Vault+GitHub, puis patch CR → `ready` | Non (Deployment séparé ; peut vivre plus tard dans un package adjacent) |
| **Operator (controller)** | Reconcile `IssueResolution` : spawn/surveille Jobs, avance `status.phase`, jamais de secrets | **Oui — cœur de ce repo** |
| **Job 1 (triage)** | Analyse texte-seul, commente le plan, label `agent: pending-validation`, écrit le diagnostic, meurt | Image / entrypoint définis ici ; exécuté comme Job |
| **Job 2 (fix)** | Génère / teste sous Sysbox, push + PR ; secrets via init-container Vault uniquement | Idem |
| **Vault** | K8s auth + secrets dynamiques GitHub / LLM | Infra cluster, hors opérateur |
| **Humain** | Gate #1 : commentaire `"agent go"` ; gate #2 : review/merge PR | — |

**Règle d'or pour l'opérateur :** il ne parle jamais à GitHub ni à Vault. Il lit/écrit uniquement des CRs et des Jobs. Les Jobs et le webhook receiver sont les seuls clients Vault/GitHub.

---

## 3. Workflow Human-in-the-Loop (HITL)

### Étape 1 — Création et triage (Job 1)

1. Ouverture d'issue → webhook → création de la CR (`metadata.name = issue-<n>` = dedup).
2. L'opérateur lance **Job 1** (triage).
3. Job 1 analyse l'issue en **texte seul** (pas d'exécution de code), commente le plan d'action, applique le label GitHub `agent: pending-validation`, écrit le diagnostic (ex. `/dev/termination-log`), et meurt.
4. L'opérateur détecte la fin du Job (`OwnerReference` → reconcile), lit le diagnostic, met à jour `status` de la CR → phase **PendingValidation**.

### Étape 2 — Barrière humaine

- L'issue reste en attente. **Aucun pod idle.**
- Un humain vérifie le plan et, s'il est sûr, commente : `"agent go"`.

### Étape 3 — Contrôle d'admission (Webhook Receiver — hors opérateur)

1. GitHub envoie `issue_comment` vers le cluster.
2. Le receiver valide la signature HMAC.
3. Il s'auth auprès de Vault, interroge l'API GitHub (token lecture) pour vérifier que l'auteur est dans `CODEOWNERS`.
4. Si OK → patch CR vers **Ready** (ex. label/status `agent: ready` / `spec.approved` / `status.phase`).

### Étape 4 — Exécution Sysbox (Job 2)

1. L'opérateur voit `Ready` et lance **Job 2**.
2. Job 2 génère le code, exécute et teste sous **Sysbox**.
3. Push branche + ouverture PR (validation humaine finale = gate #2).
4. L'opérateur observe la fin du Job → `status.phase = PROpen` (+ `status.prURL`).

---

## 4. Contrat de la CR `IssueResolution`

```yaml
apiVersion: hal.dev/v1alpha1
kind: IssueResolution
metadata:
  name: issue-1234          # = issue number → dedup etcd
spec:
  issueNumber: 1234
  # Desired state écrite par le webhook receiver (pas par l'opérateur)
  approved: false           # true après "agent go" + CODEOWNERS OK
status:
  phase: Triage             # voir machine d'états ci-dessous
  triage: { inScope: true, suspicious: false, summary: "..." }
  planCommentURL: ""
  prURL: ""
  conditions: []            # Triaged, AwaitingApproval, Ready, PROpen, Failed
  observedGeneration: 1
```

### Machine d'états (`status.phase`)

```
Triage → PendingValidation → Ready → Executing → PROpen → Done
                |                 |
                v                 v
            Rejected           Failed
```

| Phase | Qui y entre | Action opérateur |
|---|---|---|
| `Triage` | CR créée | Créer Job 1 s'il n'existe pas ; attendre |
| `PendingValidation` | Job 1 Succeeded + in-scope | **Requeue / no-op** tant que `spec.approved == false` |
| `Ready` | Webhook receiver (après `"agent go"`) | Créer Job 2 |
| `Executing` | Job 2 lancé | Surveiller Job 2 |
| `PROpen` | Job 2 Succeeded | Écrire `status.prURL` ; attendre merge humain (optionnel) |
| `Rejected` | Job 1 out-of-scope / suspicious | Stop |
| `Failed` | Job Failed / diagnostic invalide | Stop + condition `Failed` |

Transitions **écrites par l'opérateur** : tout sauf `Ready` (écrit par le webhook receiver via `spec.approved` / patch phase).

---

## 5. Contrat reconcile (ce que l'opérateur doit faire)

À chaque reconcile, pour une `IssueResolution` :

1. Lire `status.phase` + Jobs owned (`OwnerReference`).
2. Prendre **le plus petit pas** vers l'état désiré (level-triggered, idempotent).
3. Ne jamais relancer un Job déjà `Succeeded` pour la même phase.
4. Sur Job `Failed` → `status.phase = Failed` + condition lisible.
5. Sur Job 1 `Succeeded` → parser le résultat (termination-log / annotation / ConfigMap résultat) → `PendingValidation` ou `Rejected`.
6. Si `PendingValidation` et `!spec.approved` → return + requeueAfter (pas de pod qui attend).
7. Si `spec.approved` (ou phase `Ready`) → créer Job 2 si absent.
8. Sur Job 2 `Succeeded` → `PROpen` + `prURL`.

**Ce que l'opérateur ne fait jamais :**

- Appeler GitHub ou Vault
- Contenir des secrets dans le `spec` / `status`
- Faire tourner du code généré dans le process controller
- Idler un worker pendant la barrière humaine

---

## 6. Sécurité (contraintes que l'opérateur doit respecter)

### Secrets (JIT & K8s auth) — côté Jobs, pas controller

- Jobs s'auth Vault via `auth/kubernetes` (JWT du ServiceAccount).
- Tokens GitHub dynamiques (`github/` engine), TTL court (~5 min), scopes minimaux :
  - Job 1 : `issues:write` (comment + label)
  - Job 2 : `contents:write` + `pull_requests:write` (push + PR) — jamais merge/admin
- **Aucun secret dans la CR** (lisible par RBAC `get` dans etcd).

### Isolation Job 2

- `runtimeClassName: sysbox-runc` — Docker/KinD sans `--privileged`.
- **NetworkPolicy :** egress = GitHub + endpoint LLM uniquement. **Pas** d'accès à l'API Kubernetes ni à Vault depuis le conteneur principal.
- **Init-container** : auth Vault → écrit le token GitHub (+ clé LLM si besoin) dans un volume partagé → le conteneur principal n'a plus accès Vault.
- Limites CPU/RAM ; user namespaces Sysbox pour les workloads imbriqués.

### Mitigation prompt-injection

- Job 1 = analyse seule, zéro exécution de code → un plan aberrant est bloqué par le CODEOWNER (`"agent go"`).
- Job 2 n'existe qu'après gate humaine cryptographiquement + hiérarchiquement validée.
- Le modèle ne voit jamais le token GitHub publish (séparation temporelle init vs run, ou phase model puis phase publish).

---

## 7. Surfaces hors scope immédiat de l'opérateur

À construire **après** le squelette controller + CRD + reconcile stub :

- Webhook Receiver (HMAC + CODEOWNERS + patch CR)
- Images Job 1 / Job 2 + `CodeFixProvider`
- Manifests Vault / NetworkPolicy / Sysbox RuntimeClass
- Dashboard (optionnel) — le gate primaire ici est le commentaire GitHub `"agent go"`, pas une UI

---

## 8. Premier livrable opérateur (slice A → B)

1. **Slice A — Skeleton :** module Go + Kubebuilder, CRD `IssueResolution`, reconciler stub.
2. **Slice B — State machine :** transitions ci-dessus avec Jobs factices (busybox) + OwnerReference.
3. **Slice C — Vrais Jobs :** brancher images triage/fix + termination-log + conditions riches.
)
