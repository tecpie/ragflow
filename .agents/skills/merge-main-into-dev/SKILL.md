---
name: merge-main-into-dev
description: >-
  Merge latest upstream/origin main into tecpie/dev and resolve conflicts without
  dropping local features. Use when the user asks to merge main into dev, sync
  upstream/main to dev, 把 main 合并到 dev, or bring official main into tecpie/dev.
---

# Merge main into tecpie/dev

## Goal

Bring official `main` into `tecpie/dev` while **keeping tecpie local features**.
Official EE/OSS ownership moves do **not** authorize deleting still-used local code.

Historical merge intent (keep this):

> Combine official main with local features … without dropping either side.

## Remotes

| Remote | Repo | Role |
|--------|------|------|
| `upstream` | `infiniflow/ragflow` | Official source of truth for `main` |
| `origin` | `tecpie/ragflow` | Local `dev` / `main` mirror |

Prefer `upstream/main` (or `origin/main` when it matches). Do **not** push unless the user asks.

## Procedure

1. Stash only tracked WIP that would block checkout/merge. Leave `.tmp-deploy/`, secrets, and large binaries alone.
2. `git fetch upstream main` and `git fetch origin main dev`.
3. On `dev`: fast-forward to `origin/dev` when behind (`git pull --ff-only origin dev`).
4. `git merge origin/main` (or `upstream/main` when identical).
5. Resolve every conflict with the rules below.
6. After resolve: compile or run the narrowest relevant package tests before committing the merge.
7. Commit the merge. Restore the stash. Do not push unless asked.
8. Summarize what landed from main and which local features were preserved in conflicts.

## Conflict rules (mandatory)

### Default

- Prefer **both sides**: take main’s fix/refactor **and** keep `dev` behavior.
- Prefer **dev** when a conflict is local product capability vs official cleanup/EE split.

### modify/delete (main deleted, HEAD modified)

1. Search for remaining callers on `dev` (`service` / `handler` / `router` / `dao` / frontend).
2. If callers remain → **keep the file** (restore from pre-merge `dev`).
3. If main only added an EE stub hook → keep the stub call site **and** provide a real `!ee` implementation for tecpie, or keep the previous inline logic. Never leave a no-op stub while local APIs still exist.
4. Official “moved to EE” / “remove enterprise ownership from OSS” is **not** a reason to delete tecpie features that still ship on `dev`.

Known local features that must survive main merges unless the user explicitly retires them:

- Evaluation stack (`internal/entity/evaluation.go`, `internal/service/evaluation*.go`, `internal/handler/evaluation.go`, `/evaluations` routes, dao AutoMigrate / JSON-column registration, user-delete cleanup)
- Ontology / DocCompile paths
- Table/signature vision enhance flags
- Metadata value_source prefetch
- Local i18n / operation-log handler behavior
- Local Dockerfile mirror/`libpcre2` fixes (compose them with main’s native-libs cache changes)

### Content conflicts

| Area | Keep from `dev` | Take from `main` |
|------|-----------------|------------------|
| Dockerfile | Aliyun/`libpcre2`/tmp fixups | native-libs bind mount + gomod cache |
| visionSettings | `skipTableEnhance` / `skipSignatureEnhance` | `language` |
| ingestion consumer | `TaskTypeDocCompile` handling | `handleEvaluationTask` hook |
| handlers | auth + i18n context | `StartOperationLog` / shared response helpers |
| go.sum / go.mod | tecpie-only deps (e.g. goRDFlib) | main dependency bumps |

### EE stubs

- `//go:build !ee` stubs exist so OSS builds. On tecpie they are the open build.
- Adding/keeping a stub is fine for compile gaps.
- Replacing a working local feature with a no-op stub is **wrong**.
- Example: after main removed evaluation entity ownership, restore entity + AutoMigrate and implement `deleteEvaluationsEE` for `!ee` instead of accepting the empty stub.

## Post-merge checks

- `git diff --name-only --diff-filter=U` is empty.
- No leftover `<<<<<<<` markers in tracked source.
- At least one of: package build/test for touched areas, or `bash build.sh --test` for the packages that own restored features.
- Confirm local features still compile (especially evaluation if main touched it).

## Do not

- Blindly take main on modify/delete conflicts.
- Drop `/evaluations` or evaluation entities because official marked them EE.
- Push to `origin/dev` without an explicit user request.
- Force-push or rewrite shared history.
