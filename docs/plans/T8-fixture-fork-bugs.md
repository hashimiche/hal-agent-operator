# Plan T8 — Fixture : fork `hal` + injection de 4 bugs (mcp ×2 / Vault / MariaDB partagé)

> **Destinataire : Grok (agent d'exécution).** Plan exécutable. Aucune modification de code n'a été faite. Identifiants/chemins en anglais, explications en français.
>
> **Distribution retenue (décision utilisateur 2026-07-23) :** 1 bug **mcp** (trivial), 1 bug **Vault** (sonde OIDC), 1 bug **MariaDB partagé Vault↔Boundary** (« marche d'un côté, casse de l'autre »), + 1 bug **mcp multi-fichiers** (bug 4, restauré à la demande) qui démontre la **limite du fix mono-fichier** du Job 2. Le bug MariaDB s'inspire directement du commit de référence `5d1f6b1` *fix(vault): report OIDC via Authentik and retire Keycloak checks* (voir §2.2 et §2.3) — même thème : un **service partagé** dont l'identité (nom de conteneur / sonde) doit rester cohérente entre produits sous peine de « drift ».

---

## 0. État réel du repo cible (constaté, pas supposé)

Le fork existe : `/home/miche/git/hashicorp_academy_labs/test-hal-operator`.

- **Remote** : `git@github.com:hashimiche/test-hal-operator.git` — branche `main` (`86265a3 initial commit`), arbre propre.
- **Nature** : fork réel de `hal`, CLI HashiCorp Academy en **Go** (`module hal`, `go 1.26.1`) bâtie sur **cobra**.
- **Runner de tests** : `go test ./...` (pas de Makefile/Taskfile dans ce repo).
- **⚠️ Aucun test dans `cmd/vault` ni `cmd/boundary`.** Les seuls `*_test.go` du repo sont dans `cmd/mcp` (`mcp_test.go`, `ops_api_test.go`) et `internal/integrations` (`gitlab_test.go`). **Conséquence : pour les bugs Vault et MariaDB il faudra AJOUTER les tests _et_ un peu d'« infrastructure de test »** (seam + accesseurs exportés) — voir §3. Le patron existe déjà dans le repo : `cmd/mcp` utilise le seam `runHAL` (variable de package remplacée dans les tests). On applique le même patron.
- **CI** (`.github/workflows/ci.yml`) : jobs `build/vet/test` (`go build/vet/test ./...`), `lint` (`gofmt` + `golangci-lint --new-from-merge-base=origin/main`), `vuln` (non bloquant). **Déclencheurs : `push` sur `feature/**` et `bugfix/**` uniquement** (ni `main`, ni tags, ni `pull_request`). → stratégie de branches §5.
- **CODEOWNERS** : `* @hashimiche` (+ règles spécifiques dont `/cmd/mcp/`). **Toutes** les PRs de correction exigent donc la review humaine de `@hashimiche` → la porte « merge humain » est automatique.
- **Toolchain** : `go1.26.1` s'auto-télécharge au premier run → réseau + `GOPATH`/`~/go/pkg` inscriptibles requis (OK en CI GitHub ; en sandbox l'écriture de `~/go/pkg/sumdb` peut échouer). Cf. checklist §7.

---

## 1. Vue d'ensemble des 3 bugs

| # | Produit | Difficulté | Fichier(s) | Fonction / symbole | Test |
|---|---|---|---|---|---|
| 1 | mcp | Trivial (typo) | `cmd/mcp/mcp.go` | `isAllowedOrigin` | `TestIsAllowedOrigin` (**existant**) |
| 2 | Vault | Moyen (régression sonde OIDC, cf. commit `5d1f6b1`) | `internal/global/status_snapshot.go` | sonde `oidc` du produit `vault` dans `BuildStatusSnapshot` | `TestVaultOIDCProbesAuthentik` (**nouveau** + seam) |
| 3 | MariaDB partagé | Cross-produit (marche côté Vault, casse côté Boundary) | `cmd/boundary/defaults.go` (+ accesseurs) | miroir `vaultMariaDBContainer` consommé par `hal boundary mariadb --with-vault` | `TestSharedVaultMariaDBEndpoint` (**nouveau**, cross-package) |
| 4 | mcp | Difficile (2 fichiers, **limite du fix mono-fichier**) | `cmd/mcp/advanced.go` **+** `cmd/mcp/ops_api.go` | `validateCommand` + handler `get_vault_pki_status` | `TestVaultPkiRecommendationsSurfaced` (**nouveau**) + `TestValidateCommandReflectsCurrentSurface` (existant) |

Chaque bug : (a) une modif source réelle, (b) un test **hermétique** (sans réseau, sans conteneur réel) RED avec le bug / GREEN après fix, (c) une issue GitHub décrivant **le symptôme** (jamais le fix), (d) critère de succès = test vert + PR mergée par un humain.

---

## 2. Détail des bugs

### 2.1 Bug 1 — mcp, trivial : typo dans `isAllowedOrigin`

**Fichier / fonction** : `cmd/mcp/mcp.go`, `isAllowedOrigin` (≈ l.637‑656). Le `switch` autorise les origines loopback :

```go
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
```

**Bug à injecter** : `"127.0.0.1"` → `"127.0.0.2"` (typo d'un caractère). Le serveur MCP streamable‑http rejette alors l'origine loopback légitime `http://127.0.0.1:<port>`.

**Test qui vire au RED** : `TestIsAllowedOrigin` (**existant**, `cmd/mcp/mcp_test.go` ≈ l.33) contient déjà :
```go
{origin: "http://127.0.0.1:8080", want: true},
```
→ Avec le bug : renvoie `false` ⇒ **RED**. Après fix ⇒ **GREEN**. Aucun test à ajouter.

**Commande de repro** : `go test ./cmd/mcp/ -run TestIsAllowedOrigin`

**Issue GitHub** (symptôme uniquement) :
- **Title** : `bug: MCP streamable-http rejects loopback origin 127.0.0.1`
- **Body** :
  ```markdown
  ### Description
  Le serveur MCP (transport streamable-http) refuse les requêtes dont l'en-tête
  `Origin` est `http://127.0.0.1:<port>`, alors que les clients locaux (boucle
  locale) devraient toujours être autorisés. `http://localhost:...` fonctionne,
  mais `http://127.0.0.1:...` non — incohérent.

  ### Steps to reproduce
  1. `hal mcp serve --transport streamable-http --http-port 8080`
  2. POST `initialize` avec l'en-tête `Origin: http://127.0.0.1:5173`
  3. Observer la réponse HTTP.

  ### Expected / Actual
  Attendu : requête acceptée (HTTP 200). Constaté : `403 Forbidden` ("origin not allowed").

  ### OS / platform
  Linux (WSL2)
  ```

**Critère de succès** : `TestIsAllowedOrigin` vert + PR (`bugfix/**`) mergée par un humain. **Fix mono-fichier** (illustre le cas que le Job 2 sait traiter).

---

### 2.2 Bug 2 — Vault, moyen : la sonde OIDC probe le mauvais conteneur (régression du commit `5d1f6b1`)

**Contexte (commit de référence)** : `5d1f6b1` a corrigé `hal status` / `hal vault status` qui sondaient l'ancien conteneur `hal-keycloak` au lieu de `hal-authentik-server` pour la feature OIDC de Vault. Dans le fork (post-fix), l'état correct est présent aux **deux** endroits :
- `cmd/vault/status.go` l.75 : `{"OIDC (Authentik)", integrations.AuthentikServerContainer, "oidc"}` ✅
- `internal/global/status_snapshot.go` l.59 : `"oidc": BoolState(CheckContainer(engine, "hal-authentik-server"))` ✅ (nom en dur)

**Bug à injecter (régression, 1 ligne)** : dans `internal/global/status_snapshot.go` l.59, remettre l'ancien conteneur :
```go
"oidc": BoolState(CheckContainer(engine, "hal-keycloak")),
```
Effet : le snapshot de santé (servi par `hal-health`, consommé par HAL Plus / MCP) reporte l'OIDC Vault comme **désactivé** même quand Authentik tourne, car `hal-keycloak` n'existe plus.

> Pourquoi `internal/global/status_snapshot.go` et pas `cmd/vault/status.go` ? Parce que c'est la sonde **testable hermétiquement** : `BuildStatusSnapshot` est une fonction pure-ish (elle n'écrit rien, retourne du JSON) dont la seule dépendance runtime est `CheckContainer`. On la rend testable via un **seam** (§3.1). `cmd/vault/status.go` est un `cobra.Command.Run` non testable sans conteneur. Fonctionnellement, la sonde OIDC du produit Vault vit ici.

**Test à AJOUTER** — `internal/global/status_snapshot_test.go`, `TestVaultOIDCProbesAuthentik` (dépend du seam §3.1) :

```go
package global

import (
	"encoding/json"
	"testing"
)

func TestVaultOIDCProbesAuthentik(t *testing.T) {
	// Seam: simulate a live engine where Vault + Authentik run, Keycloak does not.
	origCheck, origMP := CheckContainer, CheckMultipass
	defer func() { CheckContainer, CheckMultipass = origCheck, origMP }()

	running := map[string]bool{
		"hal-vault":             true,
		"hal-authentik-server":  true,
		// "hal-keycloak" absent → not running
	}
	CheckContainer = func(engine, name string) bool { return running[name] }
	CheckMultipass = func(name string) bool { return false }

	raw, err := BuildStatusSnapshot("docker")
	if err != nil {
		t.Fatalf("BuildStatusSnapshot: %v", err)
	}
	var snap StatusSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var oidc string
	for _, p := range snap.Products {
		if p.Product != "vault" {
			continue
		}
		for _, f := range p.Features {
			if f.Feature == "oidc" {
				oidc = f.State
			}
		}
	}
	if oidc != "enabled" {
		t.Errorf("vault oidc feature = %q, want \"enabled\" (must probe hal-authentik-server, not the retired hal-keycloak)", oidc)
	}
}
```

Avec le bug (probe `hal-keycloak`) : `running["hal-keycloak"]` = false ⇒ `oidc == "disabled"` ⇒ **RED**. Après fix ⇒ **GREEN**.

> Note hermétique : `BuildStatusSnapshot` appelle aussi `resolveVaultAudit` (qui `exec` un `vault audit list` dans `hal-vault`). En test sans conteneur, cet appel échoue proprement et renvoie `"unknown"` — sans effet sur l'assertion `oidc`. Pas de réseau. (Si l'on veut zéro spawn de process, seamer aussi `resolveVaultAudit`, mais non requis pour ce test.)

**Commande de repro** : `go test ./internal/global/ -run TestVaultOIDCProbesAuthentik`

**Issue GitHub** (symptôme uniquement) :
- **Title** : `bug: hal status reports Vault OIDC as down even when Authentik is running`
- **Body** :
  ```markdown
  ### Description
  Le snapshot de santé (`hal status`, hal-health / HAL Plus / MCP) affiche la
  feature Vault "oidc" comme désactivée alors que la stack Authentik
  (hal-authentik-server) tourne et que l'OIDC Vault est bien configuré.
  `hal vault status` affiche correctement l'OIDC comme actif — les deux vues
  se contredisent.

  ### Steps to reproduce
  1. `hal vault create` puis `hal vault oidc --enable` (déploie Authentik).
  2. Vérifier que `hal-authentik-server` tourne.
  3. Comparer `hal vault status` (OIDC up) et `hal status` (OIDC down).

  ### Expected / Actual
  Attendu : les deux vues reportent l'OIDC Vault actif quand Authentik tourne.
  Constaté : le snapshot global reporte "oidc: disabled" (sonde un conteneur
  qui n'existe plus).

  ### OS / platform
  Linux (WSL2)
  ```

**Critère de succès** : `TestVaultOIDCProbesAuthentik` vert + PR mergée. **Fix mono-fichier** (1 ligne dans `status_snapshot.go`).

---

### 2.3 Bug 3 — MariaDB partagé : Vault marche, Boundary `--with-vault` casse

**Le couplage réel (constaté)** : MariaDB est un service que **deux produits** touchent :

- **Vault** possède/crée le conteneur `hal-vault-mariadb` :
  - `cmd/vault/defaults.go` : `vaultMariaDBContainer = "hal-vault-mariadb"`, `vaultMariaDBPort = 3306`.
  - `cmd/vault/database.go` l.100‑107 : démarre `hal-vault-mariadb` (`-p 3306:3306`) ; l.91 : `connection_url` des creds dynamiques Vault = **littéral en dur** `"{{username}}:{{password}}@tcp(hal-vault-mariadb:3306)/"`.
- **Boundary** *référence* (ne possède pas) ce conteneur pour son mode `--with-vault` :
  - `cmd/boundary/defaults.go` : **miroir** `vaultMariaDBContainer = "hal-vault-mariadb"` (dupliqué, commentaire « referenced (not owned) here when --with-vault attaches to that database »).
  - `cmd/boundary/mariadb.go` l.73/99 : en `--with-vault`, `dbContainerName = vaultMariaDBContainer` → Boundary enregistre l'hôte cible = ce miroir, et branche les creds dynamiques Vault (`database/creds/dba-role`) via `linkBoundaryToVault`.

**Invariant** : le nom (et port) du MariaDB partagé doit être **identique** côté Vault (source de vérité) et côté Boundary (miroir). S'ils divergent, `hal boundary mariadb --with-vault` pointe vers un hôte inexistant → échec, alors que `hal vault database enable` continue de marcher. C'est exactement le « drift de service partagé » que le commit `5d1f6b1` combat (là : GitLab/Authentik partagés ; ici : MariaDB partagé).

**Bug à injecter (1 ligne, côté consommateur)** : dans `cmd/boundary/defaults.go`, faire dériver le miroir :
```go
vaultMariaDBContainer = "hal-vault-maria" // typo : devrait être "hal-vault-mariadb"
```
Effet « marche d'un côté, casse de l'autre » :
- **Côté Vault** : `hal vault database enable` crée et exploite `hal-vault-mariadb` → **OK**.
- **Côté Boundary** : `hal boundary mariadb --with-vault` cible l'hôte `hal-vault-maria` (inexistant) → la cible Boundary ne peut pas se connecter au MariaDB → **KO**.

**Infrastructure de test à ajouter (§3.2)** — deux accesseurs exportés purs, puis un test cross-package :

```go
// cmd/vault/helper.go  (ou database.go) — expose la source de vérité, pure.
func MariaDBEndpoint() (host string, port int) { return vaultMariaDBContainer, vaultMariaDBPort }

// cmd/boundary/mariadb.go — expose ce que le mode --with-vault cible réellement, pure.
func VaultAttachEndpoint() (host string, port int) { return vaultMariaDBContainer, boundaryMariaDBPort }
```

```go
// cmd/boundary/shared_mariadb_test.go
package boundary

import (
	"testing"

	"hal/cmd/vault"
)

// hal boundary mariadb --with-vault attaches to the Vault-owned MariaDB.
// Boundary's mirror of the shared container identity must match Vault's source
// of truth, otherwise --with-vault targets a host that does not exist.
func TestSharedVaultMariaDBEndpoint(t *testing.T) {
	wantHost, wantPort := vault.MariaDBEndpoint()
	gotHost, gotPort := VaultAttachEndpoint()

	if gotHost != wantHost {
		t.Errorf("boundary --with-vault host = %q, want %q (Vault owns this container)", gotHost, wantHost)
	}
	if gotPort != wantPort {
		t.Errorf("boundary --with-vault port = %d, want %d", gotPort, wantPort)
	}
}
```

Avec le bug (`hal-vault-maria` côté Boundary) : `gotHost != wantHost` ⇒ **RED**. Après fix (miroir remis à `hal-vault-mariadb`) ⇒ **GREEN**. Test 100 % hermétique (compare des constantes, aucun conteneur). Pas de cycle d'import (`cmd/vault` n'importe pas `cmd/boundary`).

> **Port** : Vault expose `vaultMariaDBPort = 3306` ; Boundary enregistre sa cible sur `boundaryMariaDBPort = 3306` → les ports coïncident aujourd'hui, l'assertion port passe. Le bug injecté porte sur l'**hôte** (le drift réaliste). L'assertion port est là comme garde-fou de régression.

**Commande de repro** : `go test ./cmd/boundary/ -run TestSharedVaultMariaDBEndpoint`

**Issue GitHub** (symptôme uniquement) :
- **Title** : `bug: hal boundary mariadb --with-vault cannot reach the Vault MariaDB target`
- **Body** :
  ```markdown
  ### Description
  `hal vault database enable` déploie et exploite correctement le MariaDB partagé.
  Mais `hal boundary mariadb --with-vault` échoue à joindre ce même MariaDB : la
  cible Boundary est créée avec un hôte qui ne correspond pas au conteneur
  réellement démarré par Vault. Résultat : la connexion Boundary → MariaDB (via
  creds dynamiques Vault) ne s'établit pas, alors que côté Vault tout est vert.

  ### Steps to reproduce
  1. `hal vault create && hal vault database enable`   (MariaDB partagé up, OK)
  2. `hal boundary create`
  3. `hal boundary mariadb --with-vault`
  4. Tenter `boundary connect mysql ... -target-id <id>`

  ### Expected / Actual
  Attendu : Boundary cible le conteneur MariaDB géré par Vault et la connexion
  aboutit. Constaté : la cible pointe vers un hôte inexistant ; connexion KO.
  Le service MariaDB « marche côté Vault mais pas côté Boundary ».

  ### OS / platform
  Linux (WSL2)
  ```

**Critère de succès** : `TestSharedVaultMariaDBEndpoint` vert + PR mergée.

**Variante « difficile / multi-fichiers » (optionnelle — décision D2)** : le bug 4 (§2.4) couvre déjà la démonstration « limite du fix mono-fichier ». Si l'on préférait la porter ici plutôt que dans mcp, établir une **source de vérité unique** dans `internal/global` (ex. `global.VaultMariaDBContainer`, `global.VaultMariaDBPort`) consommée par Vault **et** Boundary, et injecter deux dérives (miroir Boundary **et** littéral `hal-vault-mariadb:3306` de `cmd/vault/database.go` l.91). Redondant avec le bug 4 → non retenu par défaut.

---

### 2.4 Bug 4 — mcp, difficile (2 fichiers) : Vault PKI perdu de la surface de commandes

> **But pédagogique** : prouver qu'une réécriture « un seul fichier complet » (contrainte du Job 2, cf. `LLM_CONTEXT.md` « full file contents, not a diff ») **ne suffit pas** — la correction exige d'éditer **deux** fichiers. Bug restauré à la demande ; design déjà validé lors de l'exploration `cmd/mcp`.

**Invariant réel cassé** : la surface de commandes valides (`validateCommand`, `advanced.go`) et les commandes recommandées par les outils de statut (`ops_api.go`) doivent rester cohérentes — une commande recommandée doit survivre à `sanitizeRecommendedCommands`, qui rejette silencieusement toute commande `hal ...` invalide selon `validateCommand`.

**Fichier A — `cmd/mcp/advanced.go`**, `validateCommand`, table `validProducts` (≈ l.199) :
```go
		"vault": {"create", "status", "delete", "update", "audit", "oidc", "jwt", "k8s", "ldap", "database", "db", "userpass", "up", "os", "pki", "obs"},
```
**Modif A** : retirer `"pki"` de la liste des sous-commandes `vault` → `validateCommand("hal vault pki ...")` devient invalide.

**Fichier B — `cmd/mcp/ops_api.go`**, `handleOpsTool`, cas `get_vault_pki_status` (≈ l.401), liste des commandes recommandées :
```go
		return opSuccessForTool("get_vault_pki_status", "vault pki status collected", map[string]interface{}{"execution": execRes}, []string{"hal vault pki", "hal vault pki enable", "hal vault pki enable --acme", "hal vault pki enable --k8s"}, checks, nil, nil, []string{"https://developer.hashicorp.com/vault/docs/secrets/pki"}), true
```
**Modif B** : supprimer `"hal vault pki enable"` de cette liste (ne laisser que `"hal vault pki"`).

**Pourquoi les 2 fichiers sont nécessaires pour réparer** :
- Assertion A → verte **seulement** si `advanced.go` réintègre `"pki"` (sinon `validateCommand` rejette).
- Assertion B → exige **à la fois** que `ops_api.go` réémette `"hal vault pki enable"` **et** que `advanced.go` l'accepte (sinon `sanitizeRecommendedCommands` la supprime avant l'enveloppe).
- Fix de `advanced.go` seul ⇒ B reste RED ; fix de `ops_api.go` seul ⇒ A+B restent RED. **Une réécriture mono‑fichier ne peut pas rendre le test vert.**

**Test à AJOUTER** — `cmd/mcp/fixture_hard_test.go`, `TestVaultPkiRecommendationsSurfaced` :
```go
package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVaultPkiRecommendationsSurfaced(t *testing.T) {
	// Assertion A — la surface de commandes (advanced.go) doit accepter PKI.
	if ok, _ := validateCommand("hal vault pki enable")["valid"].(bool); !ok {
		t.Errorf("validateCommand should accept 'hal vault pki enable'")
	}

	// Assertion B — l'outil PKI (ops_api.go) doit exposer la commande d'activation,
	// et elle doit survivre à sanitizeRecommendedCommands (qui appelle validateCommand).
	restore := runHAL
	runHAL = func(args ...string) toolExecution {
		return toolExecution{
			Command:   "hal " + strings.Join(args, " "),
			ExitCode:  0,
			Output:    "status: ok",
			Timestamp: "2026-01-01T00:00:00Z",
		}
	}
	defer func() { runHAL = restore }()

	res, handled := handleOpsTool("get_vault_pki_status", map[string]interface{}{})
	if !handled {
		t.Fatal("get_vault_pki_status not handled")
	}
	var payload opContractResponse
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	found := false
	for _, c := range payload.RecommendedCommands {
		if c == "hal vault pki enable" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("get_vault_pki_status should recommend 'hal vault pki enable'; got %v", payload.RecommendedCommands)
	}
}
```

**Effet de bord attendu** : la modif A casse aussi le test existant `TestValidateCommandReflectsCurrentSurface` (il asserte `"hal vault pki"` valide) — acceptable, cela renforce la démonstration. La modif B n'est captée **que** par l'assertion B du nouveau test → c'est ce qui impose la 2ᵉ édition.

**Commande de repro** : `go test ./cmd/mcp/ -run 'TestVaultPkiRecommendationsSurfaced|TestValidateCommandReflectsCurrentSurface'`

**Issue GitHub** (symptôme uniquement) :
- **Title** : `bug: HAL MCP drops Vault PKI commands (validate_command + get_vault_pki_status)`
- **Body** :
  ```markdown
  ### Description
  La surface de commandes MCP a « perdu » Vault PKI. Deux symptômes liés :
  - `validate_command` avec `hal vault pki` / `hal vault pki enable` répond
    "invalide" ("unknown subcommand"), alors que la commande existe.
  - `get_vault_pki_status` ne recommande plus `hal vault pki enable` dans
    `recommended_commands`, donc l'assistant ne sait plus comment activer PKI.

  ### Steps to reproduce
  1. Outil MCP `validate_command` avec `{"command": "hal vault pki enable"}`.
  2. Outil MCP `get_vault_pki_status` ; inspecter `recommended_commands`.

  ### Expected / Actual
  Attendu : `validate_command("hal vault pki enable") → valid: true` ; et
  `get_vault_pki_status` liste `hal vault pki enable`.
  Constaté : `valid: false` ; commande absente des recommandations.

  ### OS / platform
  Linux (WSL2)
  ```

**Critère de succès** : `TestVaultPkiRecommendationsSurfaced` **et** `TestValidateCommandReflectsCurrentSurface` verts (⇒ 2 fichiers corrigés) + PR mergée par un humain. **Fix multi-fichiers obligatoire** (démontre la limite du Job 2).

---

## 3. Infrastructure de test à ajouter (car Vault/Boundary n'en ont pas)

> Ces ajouts sont du **test-enabling minimal**, dans l'esprit du seam `runHAL` déjà présent dans `cmd/mcp`. Ils vont sur la branche `fixture/**` **avec** le bug et le test (§5), et restent en place après le fix (ils sont neutres pour le runtime).

### 3.1 Seam pour la sonde de statut (bug 2)
Dans `internal/global/global.go`, transformer les fonctions en variables assignables :
```go
// avant : func CheckContainer(engine, name string) bool { ... }
var CheckContainer = func(engine, name string) bool {
	out, err := exec.Command(engine, "ps", "-q", "-f", fmt.Sprintf("name=^%s$", name)).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

var CheckMultipass = func(name string) bool { /* corps inchangé */ }
```
Changement purement mécanique (comportement identique), qui rend `BuildStatusSnapshot` testable. Aucun appelant à modifier.

### 3.2 Accesseurs purs exportés (bug 3)
- `cmd/vault` : `func MariaDBEndpoint() (string, int)` (retourne `vaultMariaDBContainer, vaultMariaDBPort`).
- `cmd/boundary` : `func VaultAttachEndpoint() (string, int)` (retourne `vaultMariaDBContainer, boundaryMariaDBPort` — les valeurs réellement utilisées par le chemin `--with-vault`, cf. `mariadb.go` l.73/99).

Ces accesseurs exposent des constantes déjà utilisées par le code de prod : ils reflètent fidèlement le comportement runtime.

---

## 4. Création des issues sur le fork — `gh issue create`

Créer les 3 issues sur `hashimiche/test-hal-operator` (ce sont elles qui alimenteront le triage de l'opérateur) :

```bash
REPO=hashimiche/test-hal-operator

gh issue create --repo "$REPO" --label bug \
  --title "bug: MCP streamable-http rejects loopback origin 127.0.0.1" \
  --body-file issues/bug1-mcp.md

gh issue create --repo "$REPO" --label bug \
  --title "bug: hal status reports Vault OIDC as down even when Authentik is running" \
  --body-file issues/bug2-vault-oidc.md

gh issue create --repo "$REPO" --label bug \
  --title "bug: hal boundary mariadb --with-vault cannot reach the Vault MariaDB target" \
  --body-file issues/bug3-mariadb-shared.md

gh issue create --repo "$REPO" --label bug \
  --title "bug: HAL MCP drops Vault PKI commands (validate_command + get_vault_pki_status)" \
  --body-file issues/bug4-mcp-pki.md
```

Notes :
- `--body-file` sur des fichiers temporaires `issues/bugN-*.md` (**non committés** dans le fork ; supprimés après).
- Si `gh` refuse `--label bug` (label absent), le retirer.
- Noter les numéros `#N` renvoyés : ils nomment les CR (`issue-<N>`) et les branches de fix.

---

## 5. Stratégie git / branches (buggy vs. référence corrigée)

Contraintes : la CI ne tourne que sur `feature/**` et `bugfix/**` ; une PR de correction ne peut exister que si sa base contient le bug.

1. **`main`** = code **propre/corrigé** = référence. Ne contient jamais de bug injecté, **mais** contient l'infrastructure de test §3 (seam + accesseurs) pour que la base compile et que les tests soient verts.
   - ⇒ **Étape préalable** : ouvrir d'abord une PR `feature/test-seams` sur `main` qui ajoute uniquement le seam §3.1 et les accesseurs §3.2 (+ éventuellement les tests). Ainsi `main` reste vert et les branches `fixture/*` partent d'une base testable.
2. Par bug N : créer `fixture/bug<N>` **depuis `main`**, y committer **(a)** l'injection du bug et **(b)** le test associé. C'est l'état RED reproductible ; c'est la base que l'opérateur / Job 2 clonera pour l'issue #N.
   ```bash
   git switch main && git switch -c fixture/bug1
   # injecter le bug + (bug 2/3) ajouter le test ; commit ; push
   git switch fixture/bug1 && go test ./... # doit être ROUGE
   ```
3. **Correction** livrée par PR depuis `bugfix/bug<N>-<slug>` **→ base `fixture/bug<N>`**. Le head `bugfix/**` **déclenche la CI**, qui doit passer **GREEN**. Merge humain (review CODEOWNERS `@hashimiche`).
4. Après merge, `fixture/bug<N>` reflète l'état corrigé ; `main` reste la référence.

Remarques :
- `fixture/**` ne déclenche pas la CI (voulu : le RED se constate en local ; la CI verte se démontre sur `bugfix/**`).
- **Wiring Job 2 (T9/T10/T11)** : configurer le clone/PR de Job 2 pour cibler la base `fixture/bug<N>` (pas `main`). À reporter dans le runbook T11.

---

## 6. Documentation de la fixture (section pour le runbook Job 2)

**Repo** : `test-hal-operator` (remote `hashimiche/test-hal-operator`).

**Cibles par bug** (localiser par **nom de symbole**, pas par n° de ligne — susceptibles de bouger) :
- Bug 1 (mcp) : `cmd/mcp/mcp.go` → `isAllowedOrigin` ; test `cmd/mcp/mcp_test.go::TestIsAllowedOrigin`.
- Bug 2 (Vault OIDC) : `internal/global/status_snapshot.go` → sonde `oidc` de `BuildStatusSnapshot` ; test `internal/global/status_snapshot_test.go::TestVaultOIDCProbesAuthentik` (+ seam `internal/global/global.go`).
- Bug 3 (MariaDB partagé) : `cmd/boundary/defaults.go` → miroir `vaultMariaDBContainer` ; accesseurs `cmd/vault::MariaDBEndpoint` + `cmd/boundary::VaultAttachEndpoint` ; test `cmd/boundary/shared_mariadb_test.go::TestSharedVaultMariaDBEndpoint`.
- Bug 4 (mcp multi-fichiers) : `cmd/mcp/advanced.go` → `validateCommand` **+** `cmd/mcp/ops_api.go` → handler `get_vault_pki_status` ; test **nouveau** `cmd/mcp/fixture_hard_test.go::TestVaultPkiRecommendationsSurfaced` (+ existant `TestValidateCommandReflectsCurrentSurface`).

**Reproduire RED / GREEN** (nécessite réseau + toolchain go1.26.1 auto-téléchargée) :
```bash
cd test-hal-operator
git switch fixture/bug1 && go test ./cmd/mcp/       -run TestIsAllowedOrigin            # FAIL
git switch fixture/bug2 && go test ./internal/global/ -run TestVaultOIDCProbesAuthentik  # FAIL
git switch fixture/bug3 && go test ./cmd/boundary/  -run TestSharedVaultMariaDBEndpoint  # FAIL
git switch fixture/bug4 && go test ./cmd/mcp/       -run 'TestVaultPkiRecommendationsSurfaced|TestValidateCommandReflectsCurrentSurface'  # FAIL
# après fix (sur la branche bugfix/**) :
go test ./...   # tout vert
```

**Référence par le runbook Job 2** : pour chaque issue `#N`, Job 2 (a) clone la base `fixture/bug<N>`, (b) constate le test RED, (c) réécrit le(s) fichier(s) cible(s), (d) `go test ./...` GREEN, (e) ouvre une PR `bugfix/bug<N>-<slug>` → merge humain. Lister par bug : fichier attendu, nom du test, commande `go test -run`.

---

## 7. À vérifier AVANT de confier à Grok (checklist)

1. **Fork accessible** : `git -C test-hal-operator remote -v` → `hashimiche/test-hal-operator`, `main`, arbre propre.
2. **Toolchain Go** : réseau + `GOPATH`/`~/go/pkg` inscriptible (go1.26.1 + modules se téléchargent au 1er run). Valider `go build ./...`.
3. **Baseline verte** : `go test ./...` passe **avant** toute injection.
4. **`gh` authentifié** avec droit d'écriture d'issues sur `hashimiche/test-hal-operator` (`gh auth status`).
5. **Symboles cibles présents** (vérifiés durant l'exploration) : `isAllowedOrigin` + `TestIsAllowedOrigin` ; sonde `oidc` dans `BuildStatusSnapshot` (`internal/global/status_snapshot.go`) ; miroir `vaultMariaDBContainer` dans `cmd/boundary/defaults.go` ; `vaultMariaDBContainer`/`vaultMariaDBPort` dans `cmd/vault/defaults.go` ; table `validProducts` (entrée `vault` avec `"pki"`) dans `cmd/mcp/advanced.go` + handler `get_vault_pki_status` (liste incluant `"hal vault pki enable"`) + `TestValidateCommandReflectsCurrentSurface` dans `cmd/mcp`.
6. **Ordre d'exécution** : d'abord la PR `feature/test-seams` (§5.1) sur `main`, puis les 4 `fixture/bug<N>`.
7. **CODEOWNERS** : `* @hashimiche` ⇒ review humaine sur les 3 PRs (porte « merge humain » automatique). S'assurer que l'auteur des fix n'est pas @hashimiche si l'on veut une vraie review.
8. **Déclencheurs CI** : branches de correction préfixées `bugfix/` (ou `feature/`) sinon pas de preuve CI.
9. **`gofmt`** : le job `lint` échoue sur tout fichier non formaté (nouveaux tests + seam). `gofmt -w` avant de pousser.
10. **Wiring Job 2** : documenter que Job 2 cible la base `fixture/bug<N>` (pas `main`).
11. **Nettoyage** : fichiers `issues/bug*-*.md` (pour `--body-file`) non committés.

---

## Décisions (assumées / à trancher)

- **D1 — Emplacement du bug Vault** : `internal/global/status_snapshot.go` (testable via seam, sonde du produit Vault) plutôt que `cmd/vault/status.go` (cobra `Run`, non testable sans conteneur). Le commit de référence a d'ailleurs corrigé **les deux** fichiers ensemble.
- **D2 — Bug MariaDB : simple (retenu)** : drift **mono-ligne** du miroir Boundary → fix mono-fichier, cross-package testable, « marche Vault / casse Boundary ». La leçon « limite du fix mono-fichier » est désormais portée par le **bug 4 (mcp, §2.4)**, donc la variante multi-fichiers côté MariaDB n'est plus nécessaire (non retenue).
- **D3 — Difficulté du bug mcp** : typo `isAllowedOrigin` (trivial, test existant). Alternative moyenne dispo si besoin : `||`→`&&` dans `classifyContractError` (`cmd/mcp/ops_api.go`, test existant `TestScenarioCodesRunningNotDeployedAuthMissing`).
- **D4 — Seam `CheckContainer`/`CheckMultipass`** : conversion en `var` (patron `runHAL`). Si l'on refuse de toucher `internal/global`, replier le bug Vault sur un test qui asserte l'égalité du nom de conteneur sondé avec `integrations.AuthentikServerContainer` — mais cela impose d'exposer ce nom (refactor équivalent). Le seam reste la voie la plus propre.
