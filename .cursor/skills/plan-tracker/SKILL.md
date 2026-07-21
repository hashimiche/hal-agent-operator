---
name: plan-tracker
description: >-
  Track and advance project progress in LLM_PLAN.md (tasks T0-T18). Use when the
  user asks where they left off, what's next, to resume work, to check progress,
  or to mark a task done in this repo's plan of record.
disable-model-invocation: true
---

# Plan tracker (LLM_PLAN.md)

Canonical plan of record: [`LLM_PLAN.md`](../../../LLM_PLAN.md) at the repo root.
It holds a **Progress tracker** table, a **Current position** pointer, and one
section per task (`T0`–`T18`) with `To do` / `Acceptance`.

## Report where I am

1. Read `LLM_PLAN.md`.
2. The resume point = the **first task whose tracker row is `[ ]`** (matches the
   `Current position:` line).
3. Reply with: current task id + title, its `To do` bullets, its `Acceptance`,
   and the repo it targets (operator / `hal-agent-infra` / `hal` fork).

## Resume / do the next task

1. Confirm the first unchecked task; never skip a task marked `BLOCKING`.
2. Do only that task, respecting its `Files` / `To do`.
3. If it targets the operator repo, validate with:

```bash
task lint-fix && task test
```

## Mark a task done

Only after its `Acceptance` is met. Make all three edits so the file stays
consistent:

1. Progress tracker row: `| Tn | [ ] todo |` → `| Tn | [x] done |`.
2. Move the pointer: `**Current position:** ...` → the next unchecked task.
3. If the task's `Acceptance` produced notable outcomes, add a one-line note
   under its section (keep it terse).

## Rules

- `LLM_PLAN.md` is the single source of truth; `GROK_PLAN.md` is superseded
  history.
- Respect the plan's `Out of scope` list and `AGENTS.md` (never hand-edit
  generated Kubebuilder files).
- Keep edits minimal; do not restructure the plan while tracking.
