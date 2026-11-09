---
name: submit-official-pr
description: >-
  Open a feature branch from the latest infiniflow/ragflow main and submit a
  pull request to the official repo. Use when the user says 提PR给官方,
  提交官方PR, 给官方提PR, PR给官方, official PR, or upstream PR.
---

# Submit an official RAGFlow PR

Do this whenever the user asks to submit a PR to official RAGFlow. Do not improvise a different git flow.

## Remotes

| Remote | Repo | Use |
| --- | --- | --- |
| `upstream` | `infiniflow/ragflow` | PR base. Fetch only. Never push. |
| `personal` | `huang-aoqin/ragflow` | Push the feature branch here. |
| `origin` | `tecpie/ragflow` | Internal fork. Do not use for official PRs. |

`gh` must be authenticated as `huang-aoqin`.

## Rules

- Branch from a freshly fetched `upstream/main`. Never from local `dev` or any dirty checkout.
- The local `dev` checkout has unrelated WIP. Do not commit, stash, or mix those files into the PR.
- Use a new git worktree. Leave the existing checkout on its current branch.
- If `upstream/main` already contains the fix, stop. Report the commit and PR. Do not open a duplicate.
- Change only what the request needs. Match the repo style. Do not add fallback or compatibility shims.
- Pushing the feature branch to `personal` and opening the PR is authorized by this request. Do not change test servers, databases, or `upstream` itself.
- Do not commit secrets, `.env`, `.tmp-*`, or this skill.
- Do not use `Fixes` / `Closes` / `Resolves` unless the user names an issue that should auto-close.
- Do not add the `ci` label. `sep-tests` stays skipped until a maintainer adds it.
- Do not merge, and do not force-push.

## Steps

1. In `C:\workspace\AI\ragflow`:

   ```bash
   git fetch upstream main
   ```

2. Confirm the bug still exists on `upstream/main`. If it does not, report that and stop.

3. Create the worktree and branch from that commit. Slug is short and specific (`fix/gpustack-embedding-path`). Directory is a sibling of the repo:

   ```bash
   git worktree add -b <type>/<slug> C:\workspace\AI\ragflow-pr-<slug> upstream/main
   ```

   All edits, tests, and commits happen in that worktree.

4. Implement the fix. Run the narrowest test that covers it.
   - Python: `ruff format` the changed files before commit (CI `ruff-format` fails otherwise).
   - Go packages that need CGO: `bash build.sh --test ./path/...`, not raw `go test`.
   - On Windows, `go test` is acceptable only for packages that do not use CGO.

5. Commit on the feature branch. Subject states why. One concern per commit. If a hook rewrites files, make a new commit; do not amend a pushed commit.

6. Push:

   ```bash
   git push -u personal HEAD
   ```

7. Open the PR against official `main`:

   ```bash
   gh pr create --repo infiniflow/ragflow --base main --head huang-aoqin:<branch> --title "<title>" --body "<body>"
   ```

   Body:

   ```markdown
   ### Summary

   <what is wrong and what the change does>

   ### Test plan

   - [x] <command already run>
   - [ ] <check a maintainer still has to do>
   ```

8. Return the PR URL.

9. Watch review bots, including CodeRabbit. Fix real defects with new commits on the same branch and push to `personal`. Do not treat a skipped `sep-tests` job as a failure. Stop when the user has the URL and any bot comments are either fixed or explicitly left as non-defects.
