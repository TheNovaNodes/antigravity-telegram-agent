---
name: fatality-protocol
description: >-
  Activate this skill at the end of a session or when the user mentions ANY variation of "устрой фаталити", "фаталити", "fatality", "finish him", or asks to wrap up the session. This executes a strict State-Machine pipeline to clean up, document, and gracefully shut down.
---

# 🩸 FATALITY PROTOCOL (v2.1 - Fallbacks & No-Code Ready)

This is the ultimate Graceful Shutdown protocol. It adapts to the type of session (Code vs No-Code) and has built-in fallbacks if subagents are offline.

## 🔀 Step 0: Session Routing

Before executing the pipeline, analyze what happened in this session:
- **Code Session:** You wrote or modified source code. Proceed with the full Git Pipeline (Phases 1-5).
- **No-Code Session:** You only discussed architecture, researched, created skills/docs, or brainstormed without needing a codebase commit. Skip Phases 2, 3, and 4. Proceed directly to **Phase 1 (Cleanup)** and **Phase 5 (Handoff)**.

## 🚀 The State Machine Pipeline

Follow these phases sequentially. If a phase fails, transition to `HANDOFF_WRITTEN` (Log the failure) and ABORT. 

### Phase 1: Local Pre-Flight (`PREFLIGHT`)
- **Action:** 
  1. Remove all temporary debugs (prints, unresolved TODOs, hardcoded secrets) from the workspace.
  2. Sync `README.md` and documentation with decisions made today.
  3. (Code Session Only) Run `pytest` and linters.
- **Exit Criteria:** Local workspace is clean.

### Phase 2: PR Packaging (`PR_OPENED`) - *Code Sessions Only*
- **Action:** Delegate to **Jules** (`delegate_task_to_jules`) to create a branch, commit using Conventional Commits, and open a Pull Request.
- **FALLBACK:** If Jules is unavailable, unresponsive, or errors out, YOU (Trickster) must create the branch, commit, and push using local Git and `gh` CLI commands.
- **Exit Criteria:** PR exists. Branch is pushed.

### Phase 3: Validation & Audit (`AUDIT_REQUIRED` -> `CI_WAITING`) - *Code Sessions Only*
- **Action:** Delegate audit to **Manus** (via outsourcer skill). Await GitHub Actions CI on the PR.
- **FALLBACK 1 (Cloudflare):** If Manus is unavailable, use the Cloudflare AI keys (`cf` tokens from the vault) to run a fast automated audit (e.g., via Llama 3 on CF Workers).
- **FALLBACK 2 (Trickster):** If both cloud services are down, YOU (Trickster) must perform a strict security and architecture audit on the diff yourself.
- **Exit Criteria:** CI is Green. Audit is approved.

### Phase 4: Merge & CD (`MERGED` -> `DEPLOYED`) - *Code Sessions Only*
- **Action:** Execute Squash and Merge via GitHub CLI (`gh pr merge --squash`). 
- **Exit Criteria:** The specific tested `commit SHA` is merged to `main`. 

### Phase 5: The Afterparty & Handoff (`SYNC_PENDING` -> `CLOSED`)
- **Action:** 
  1. Generate `.agents/SESSION_HANDOFF.md` detailing: what was done, what was discussed, and the explicit next steps for tomorrow.
  2. (Code Session Only) `git checkout main`, `git pull --ff-only`, delete local branch.
  3. Close related old issues.
- **Exit Criteria:** Workspace is zero-inbox. Handoff is ready.
