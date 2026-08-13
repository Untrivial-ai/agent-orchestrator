# Workspace Child Default Branch Design

## Scope

Issue #2329 requires each direct child repository registered under a workspace project to retain its own default branch. This change detects that branch during workspace registration, persists it with the child repository record, and exposes it through the existing project-detail API and CLI DTOs.

Child worktree materialization is not present on `main`. The later workspace lifecycle change will consume this persisted value when selecting each child worktree's base branch; this change does not introduce or alter lifecycle behavior.

## Data model

Add a non-null `default_branch` column to `workspace_repos` in a new SQLite migration. Existing rows receive `main`, matching the current system fallback and avoiding nullable handling. Add `DefaultBranch` to `domain.WorkspaceRepoRecord` and carry it through sqlc queries and store mappings.

## Registration and reads

When `detectWorkspaceChildren` accepts a child repository, it resolves the default branch with the existing `resolveDefaultBranch` helper. That helper prefers `origin/HEAD` and falls back to the checked-out branch, avoiding accidental persistence of a feature branch when remote default metadata is available.

The child validation already rejects repositories whose branch cannot be identified. The detected branch is therefore stored as a non-empty value. `workspaceReposFromRecords` exposes it as `defaultBranch` in each workspace repository object. The CLI's mirrored response DTO carries the same field; current human-readable output remains unchanged unless an established nearby format makes displaying it necessary.

## Generated contracts

The service DTO is the code-first API source used by the controller schema generator. Regenerate and commit the OpenAPI document and frontend TypeScript schema after adding the field. Regenerate sqlc output from the migration and query changes; generated files are never edited manually.

## Testing

Use test-driven development for each behavior:

- A workspace child whose remote default differs from its checked-out feature branch is registered with the remote default.
- The workspace repository store round-trips `default_branch`.
- Project detail serialization includes each child repository's `defaultBranch`.
- Existing migration, project-service, controller, and CLI contract tests continue to pass.

Run targeted tests first, then the relevant backend suite, API drift checks, and frontend typecheck required by the changed generated contract.

## Compatibility and boundaries

The migration is additive and does not modify merged migrations. Existing workspace rows default to `main`. No workspace import validation, SCM observation, session teardown, or worktree lifecycle behavior changes are included.
