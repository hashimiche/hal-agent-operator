# GROK_PLAN — remaining tasks for the triage POC

> **Superseded by [`LLM_PLAN.md`](LLM_PLAN.md)** — the canonical, live plan of
> record (merges T0–T7 here + the T8–T18 roadmap). Resume work from there; this
> file is kept for the detailed history of the triage POC.
>
> **Status (2026-07-19): ALL TASKS DONE — POC validated end to end.**
> T0–T6 done by agent, `task test` green (controller coverage 74.6%).
> T7 run by the user: sample CR → triage Job → Gemini analysis in Job logs,
> `status.triage.summary` filled, phase `PendingValidation`.
>
> **Hotfix (2026-07-19, post T0):** Google retired `gemini-2.5-flash` for new
> API keys on 2026-07-09 (404 `NOT_FOUND` during T7). Default model changed to
> the rolling alias `gemini-flash-latest` in `internal/defaults/defaults.go`
> and `values.yaml` — task bodies below still mention the original ID.

> Execution plan for an AI agent (Grok). Each task is self-contained, in
> order — **T0 first, it blocks the POC**. Read [`LLM_CONTEXT.md`](LLM_CONTEXT.md)
> and [`AGENTS.md`](AGENTS.md) first (Kubebuilder rules: never hand-edit
> generated files `zz_generated.*`, `config/crd/bases/*`,
> `config/rbac/role.yaml` — use `task manifests` / `task generate`).

## Context

The pipeline is in place: `IssueResolution` CR → triage Job → LLM call →
analysis in logs + `status.triage`. The user runbook is [`POC.md`](POC.md).
**But the code still calls the Anthropic API**, and the user only has a
**Gemini** (AI Studio) key: T0 does the migration. Tasks T1+ fix weaknesses
identified during code review.

The command entry point is **`task`** (go-task, `Taskfile.yml` at the repo
root). The Makefile remains the internal Kubebuilder engine — do not call it
directly, go through `task`.

Validation command to run after **every** task:

```bash
task lint-fix && task test
```

Global criterion: `task test` green, no generated file edited by hand,
no secret written to any file.

---

## T0 — Anthropic → Gemini migration (BLOCKING) ✅ done

Replace the Claude API call with the Gemini API (Google AI Studio, plain key).

### T0.1 Triage worker — `cmd/triage/main.go`

Replace `callClaude` and the `anthropicRequest`/`anthropicResponse` structs.

**Recommended route: official Go SDK** `google.golang.org/genai`
(Go equivalent of `@google/genai` / `google-genai`; check the exact API on
[pkg.go.dev/google.golang.org/genai](https://pkg.go.dev/google.golang.org/genai)):

- **Env**: `GEMINI_API_KEY` (required — the SDK reads it natively, but keep
  the current code's explicit "missing key → clear error" check),
  `GEMINI_MODEL` (default `gemini-2.5-flash`). Remove every `ANTHROPIC_*`
  reference.
- Client: `genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendGeminiAPI})`.
- Call: `client.Models.GenerateContent(ctx, model, genai.Text(user), cfg)` with
  `GenerateContentConfig{ SystemInstruction: <existing system prompt>,
  Temperature: ptr(0), MaxOutputTokens: 1024,
  ResponseMIMEType: "application/json" }`, then read `resp.Text()`.
- `go mod tidy` after adding the dependency; check the distroless image still
  builds (`task build` + Docker build).

**Fallback route (if the dependency is a problem): direct REST**, no SDK:

- `POST https://generativelanguage.googleapis.com/v1beta/models/<model>:generateContent`,
  headers `Content-Type: application/json` + `x-goog-api-key: <key>` (never
  put the key in the URL: it would end up in logs).
- Body: `{"system_instruction":{"parts":[{"text":…}]},"contents":[{"role":"user","parts":[{"text":…}]}],"generationConfig":{"temperature":0,"maxOutputTokens":1024,"responseMimeType":"application/json"}}`.
- Response: concatenate `candidates[0].content.parts[].text`; HTTP errors come
  as `{"error":{"code":…,"message":…,"status":…}}` — surface them truncated.

**In both cases**:

- `responseMimeType: application/json` forces JSON output, but **keep
  `parseResult` as-is** (fences/prose) as a safety net.
- Log `--- calling Gemini ---` (POC.md expects it); `model` field of the
  result unchanged.

### T0.2 Controller — `internal/controller/issueresolution_controller.go`

Mechanical renames (same defaults everywhere):

| Before | After |
|---|---|
| `ClaudeSecretName` / default `claude-api` | `GeminiSecretName` / default `gemini-api` |
| `ClaudeSecretKey` / default `ANTHROPIC_API_KEY` | `GeminiSecretKey` / default `GEMINI_API_KEY` |
| `ClaudeModel` / default `claude-haiku-4-5-20251001` | `GeminiModel` / default `gemini-2.5-flash` |
| Job env `ANTHROPIC_MODEL` / `ANTHROPIC_API_KEY` | `GEMINI_MODEL` / `GEMINI_API_KEY` |

### T0.3 Manager entry point — `cmd/main.go`

Flags `--claude-*` → `--gemini-secret-name`, `--gemini-secret-key`,
`--gemini-model`; fallback env vars `CLAUDE_*` → `GEMINI_*`.

### T0.4 Helm chart — `charts/hal-k8s-operator/`

- `values.yaml`: `claude:` block → `gemini:` (`secretName: gemini-api`,
  `secretKey: GEMINI_API_KEY`, `apiKey: ""`, `createSecret: true`);
  `triage.model: gemini-2.5-flash` (free tier: 10 req/min, 250 req/day)
  with a comment: on quota exhaustion, override with
  `--set triage.model=gemini-2.5-flash-lite` (15 req/min, 1000 req/day).
- `templates/secret.yaml` and `templates/deployment.yaml` (args `--claude-*` →
  `--gemini-*`): follow the rename.
- `Taskfile.yml` already passes `--set gemini.apiKey=$GEMINI_API_KEY` — do not
  change it, that is the contract.

### T0.5 Tests and docs

- `internal/controller/issueresolution_controller_test.go`: renamed fields.
- `README.md`, `LLM_CONTEXT.md`: replace Claude/Anthropic mentions of the
  triage flow with Gemini. Delete `docs/POC-KIND.md` (superseded by `POC.md`).

### T0 acceptance

1. `grep -ri 'anthropic\|claude' --include='*.go' --include='*.yaml' --include='*.tpl' .`
   returns nothing (aside from possible historical `docs/`).
2. `task test` green.
3. `helm template charts/hal-k8s-operator --set gemini.apiKey=x` renders a
   `gemini-api` Secret with the `GEMINI_API_KEY` key and `--gemini-*` args.

---

## T1 — `readTriageResult`: only read the pod that succeeded ✅ done

**File**: `internal/controller/issueresolution_controller.go` (function `readTriageResult`)

**Problem**: the function lists the Job's pods by label and takes the first
one with a non-empty `State.Terminated.Message`. But the triage binary writes
a termination log **on failure too** (exit 1), and the Job has
`BackoffLimit: 1`. If attempt 1 fails and attempt 2 succeeds, the failed pod's
message may be read (list order is not guaranteed).

**To do**: in the loop over `ContainerStatuses`, skip containers whose
`cs.State.Terminated.ExitCode != 0`.

**Acceptance**: unit test (see T5) with two pods — one failed (exit 1, JSON
error message) and one succeeded (exit 0, valid result) — the function
returns the successful pod's result.

---

## T2 — Technical error ≠ business rejection ✅ done

**Files**: `cmd/triage/main.go` and `internal/controller/issueresolution_controller.go`

**Problem**: when the LLM's JSON is unparsable, triage returns
`inScope=false` → the controller moves the CR to `Rejected` (terminal), as if
the issue were out of scope. Same when the controller cannot read the
termination log (`termErr`).

**To do**:

1. Add a `parseError bool` field (json: `parseError,omitempty`) to
   `triageResult` (cmd/triage) and `triageJobResult` (controller).
2. `cmd/triage/main.go`: when `parseResult` fails, set `ParseError: true` in
   the best-effort result (the Job still succeeds, logs stay useful).
3. Controller, `job.Status.Succeeded > 0` branch: if `termErr != nil` **or**
   `tr.ParseError`, move the CR to `PhaseFailed` with condition
   `ConditionFailed` (Reason `TriageResultUnreadable`). `Rejected` stays
   reserved for `Suspicious || !InScope` on a valid result.
4. If you expose `parseError` in `TriageStatus`
   (`api/v1alpha1/issueresolution_types.go`), follow with `task manifests`
   and `task generate`.

**Acceptance**: envtest tests — (a) invalid termination message → phase
`Failed`, condition `Failed=True`; (b) valid result with `inScope=false` →
phase `Rejected` (behavior preserved).

---

## T3 — Replace deprecated `Requeue: true` ✅ done

**File**: `internal/controller/issueresolution_controller.go` (`default` branch of the phase switch)

**To do**: replace `ctrl.Result{Requeue: true}` with
`ctrl.Result{RequeueAfter: time.Second}` (field deprecated in
controller-runtime v0.24).

**Acceptance**: `task lint-fix` with no warning on this point, `task test` green.

---

## T4 — Deduplicate the default model ID ✅ done

**Files**: `internal/controller/issueresolution_controller.go`,
`cmd/triage/main.go`, `cmd/main.go`

**Problem** (inherited, worse if T0 copies the pattern): the default model ID
is hard-coded in 3 Go files + `values.yaml`.

**To do**: create `internal/defaults/defaults.go` with
`const GeminiModel = "gemini-2.5-flash"` and import it in the 3 Go files.
Leave `values.yaml` as-is (Helm-overridable) with a comment pointing to the
constant.

**Acceptance**: `grep -rn "gemini-2.5-flash" --include='*.go'` only matches
`internal/defaults/defaults.go`.

---

## T5 — Missing tests ✅ done

**Files**: `cmd/triage/main_test.go` (new),
`internal/controller/issueresolution_controller_test.go`

**To do**:

1. **`parseResult` (table-driven, pure Go, no envtest)**: bare JSON; JSON in
   ```` ```json ```` fences; JSON buried in prose; invalid JSON (error);
   empty summary (fallback "No summary returned by model").
2. **`truncateRunes`**: short string unchanged; long string truncated with
   `…`; multi-byte characters not cut in the middle.
3. **Envtest — rejection path**: successful Job with message
   `{"inScope":false,"suspicious":false,...}` → phase `Rejected`, condition
   `Triaged=True/Rejected`.
4. **Envtest — Job failure path**: `job.Status.Failed = 1` → phase `Failed`,
   condition `Failed=True`.
5. The tests described in T1 (two pods) and T2 (parseError).

**Acceptance**: `task test` green; `internal/controller` package coverage
≥ 70% (`go tool cover -func cover.out`).

---

## T6 — Verify the Taskfile podman flow ✅ done

**File**: `Taskfile.yml` (tasks `kind-poc-cluster`, `kind-poc-image`)

**Context**: the target machine has **podman, not docker**. The Taskfile
auto-detects the tool, exports `KIND_EXPERIMENTAL_PROVIDER=podman`, builds
with the qualified name `docker.io/library/hal-k8s-operator:poc` and loads
via `kind load image-archive`. Written but **not yet executed**.

**To do**: run `task kind-poc-cluster` then `task kind-poc-image` on the
machine (podman only) and fix the Taskfile if anything breaks (kind provider
under WSL2, image naming, archive loading).

**Acceptance**: both tasks pass without docker installed, and
`docker.io/library/hal-k8s-operator:poc` is visible via
`kind get nodes --name hal-agent` + `crictl images` inside the node (or
simply: the Deployment starts at POC.md step 3).

---

## T7 — End-to-end validation (last task) ✅ done (user, 2026-07-19)

Run [`POC.md`](POC.md) steps 1 → 5 **exactly** on a clean KinD cluster (the
`GEMINI_API_KEY` will be provided by the user in the environment — never
write it to a file).

**Acceptance (= the POC's expected result)**:

1. `kubectl apply` of the sample CR → Job `issue-1234-triage` created by the
   operator.
2. `kubectl -n hal-agent logs job/issue-1234-triage` contains the Gemini
   analysis (raw response + `inScope/suspicious/summary` JSON).
3. The CR's `status.triage.summary` is filled in and the phase is
   `PendingValidation` or `Rejected` depending on the verdict.

If a POC.md step is wrong or imprecise, **fix POC.md** so it matches observed
reality.

---

## Out of scope (do not do)

- Job 2 (fix + PR), GitHub webhook receiver, Vault integration, dashboard —
  see the roadmap in `LLM_CONTEXT.md`.
- No secrets in CRs, committed Helm values, code, or logs.
- Do not touch the generated files listed in `AGENTS.md`.
- Do not rewrite the Makefile: `Taskfile.yml` is the facade, the Makefile is
  the Kubebuilder engine.
