# Standalone Session Imports

## Goal

Complete PR #4830 by using the standalone-session model merged in PR #4851. A user can import a Codex or Claude Code conversation even when AO cannot associate it with an available Git repository.

## Destination model

AO keeps one session model with optional project ownership.

- When the source conversation belongs to an available Git repository, AO attaches the imported session to the matching registered project. If needed, the existing selected-import flow may register that repository first.
- When the source has no repository, its original folder is missing, or the folder is not a usable Git checkout, AO imports it as a projectless worker with `project_id = NULL`. The frontend presents it in the synthetic **Ad hoc agents** group introduced by PR #4851.
- Repository ambiguity remains an error. AO must not silently choose among multiple registered projects or attach history to an unrelated checkout.
- Provider identity stays in the existing native-session metadata so retries and search results open the same AO session instead of duplicating it.

## Import and resume behavior

Import only writes the dormant AO session and its native-history binding. It does not authenticate, launch an agent, create a Git worktree, or copy the provider transcript.

Resuming a project import uses the existing project workspace and Git-worktree path. Resuming a standalone import uses PR #4851's AO-managed per-session plain directory. Imported history remains visible before resume and is replayed through the existing provider controller after resume.

## User experience

The selected-session destination preview explains where the session will appear:

- an existing project,
- a project that AO will register after confirmation, or
- **Ad hoc agents** for a standalone import.

Standalone import needs one explicit confirmation but no folder picker. Existing imported standalone results open through `/sessions/:sessionId`. Project import remains available only from an individual project; there is no global bulk-import path.

## Cleanup

Remove obsolete branches, errors, copy, and tests that claim repository-less imports are unavailable. Keep repository-location recovery only where it can change a repository-backed destination. Do not duplicate PR #4851's standalone workspace or routing logic.

## Verification

Cover repository-backed imports, new-project registration, repository-less imports, missing-checkout imports, duplicate prevention, standalone open routing, dormant history display, standalone resume, project resume, cancellation, and unchanged project-scoped bulk import. Run backend, frontend, generated API drift, lint, build, and platform CI-equivalent checks. Exercise the final flow in an isolated Electron app and capture dark-theme screenshots and an MP4 for the PR.
