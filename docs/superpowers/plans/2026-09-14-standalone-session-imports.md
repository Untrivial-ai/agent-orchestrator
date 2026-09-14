# Standalone Session Imports Implementation Plan

1. Preserve the current search latency edits, merge current `origin/main`, and resolve conflicts in favor of PR #4851's nullable project ownership and standalone routing contracts.
2. Add failing service tests for selected repository-less imports, missing-checkout imports, idempotent standalone retry/open behavior, and ambiguous repository refusal.
3. Update dormant import registration to accept `project_id = NULL` for a worker import while retaining registered-project validation for project imports and batch imports.
4. Update selected destination resolution so unavailable or non-Git sources become confirmed standalone destinations; retain project registration for valid unregistered repositories and errors for ambiguity.
5. Route standalone imported sessions through the existing standalone UI target and revise destination copy, confirmation handling, and generated API types.
6. Adapt explicit Resume to create PR #4851's plain AO-managed workspace when the imported session has no project, while preserving Git worktrees and source-branch recovery for project imports.
7. Remove superseded repository-required code and copy, then review the full diff for unused APIs, UI paths, stale tests, TODOs, and generated drift.
8. Run focused regressions, backend and frontend suites, typecheck, lint, builds, API/sqlc generation checks, and workflow validation where available. Fix change-related failures.
9. Run the real Electron app in isolated dark-theme mode, exercise project import, standalone import, search, open, and resume, and capture screenshots plus a concise MP4.
10. Calculate added/deleted line totals from `origin/main...HEAD`, split test files, code files, and other files without overlap, update the PR description with the accounting block at the top and the uploaded media, push, and monitor all remote checks until complete.
