# 2. A User-scope config layer above projects, stored wholesale and resolved field-by-field

Date: 2026-07-23
Status: Proposed

## Context

Today AO has exactly **one config layer**: per-project. A project's `config` is a
nullable JSON blob on the `projects` SQLite row (`migrations/0008_add_project_config.sql`),
decoded into a typed `ProjectConfig` (`domain/projectconfig.go`). At spawn,
`effectiveAgentConfig` merges the role override (`Worker`/`Orchestrator`) over the
project's base `AgentConfig`, with role fields winning when set
(`session_manager/manager.go`). There is no config layer above the project: no global,
user, or domain config — verified by routes, filesystem refs, and `config.Load`.

This is the first brick of a multi-scope config model (the direction explored in
#2844). Claude Code carries four settings scopes (Managed/Local/Project/User); Codex
carries a layered TOML model with named profiles. AO has none above the project, so a
worker today cannot inherit "my usual agent setup" across projects — every project
re-declares its `AgentConfig` from scratch, and any value a user wants everywhere must
be copied into each project's blob.

The blocker for any scope-above-project is the **absent-vs-empty** distinction. Once a
User layer exists, an unset field on the project must mean "inherit from User," while a
deliberately cleared field must mean "drop the User value." Today the codebase resolves
this with a single dominant pattern:

- **Zero-value-means-inherit** is the rule for every `AgentConfig` field that exists
  (`Model`, `Permissions`): `effectiveAgentConfig` applies `if override.X != ""`
  (`manager.go`). Empty always means "inherit"; there is no way to explicitly clear a
  single field. (An earlier draft of this ADR referenced a `*MCPConfig` pointer field and
  a `SystemPrompt` field; neither exists in the codebase today. The pointer-for-third-state
  pattern is recorded here only as the established precedent to reach for, _if_ a future
  field ever needs per-field explicit-clear.)

There is no ADR recording the pattern; the rule lives only in code comments.

## Decision

Introduce a **User-scope config layer**, stored as a **singleton row in a new SQLite
table** (not a file, not a column on `projects`), exposing **the current `AgentConfig`**
(`model` and `permissions` — the only fields `domain.AgentConfig` carries today, per
`agentconfig.go`). Precedence is **Project over User**: the project's `AgentConfig` wins
over the User layer, mirroring Claude Code where User is the lowest-precedence settings
scope.

The surface is deliberately scoped to **exactly the fields `domain.AgentConfig` has
today** (Model, Permissions). This keeps the first PR a pure storage/transport addition
with zero changes to the domain package. When `AgentConfig` later gains fields (env,
systemPrompt, mcp — currently absent), the User layer picks them up automatically with no
schema change, because it reuses the same type end to end.

The **write** is wholesale; the **resolution** is field-by-field. The two must not be
conflated:

- **Write (wholesale).** A `PUT /api/v1/user-config` replaces the stored User
  `AgentConfig` blob in full, mirroring how `SetConfig` replaces the project config
  wholesale (`row.Config = in.Config`, `service.go`, `dto.go`: "Config replaces the
  project's stored config wholesale; a zero-value config clears it"). There is one
  `AgentConfig` per scope; setting it overwrites every field.
- **Resolution (field-by-field, zero-value-means-inherit).** This is the _existing_
  semantics of `effectiveAgentConfig` (`manager.go`), which overlays each role-override
  field over the project base only when that field is non-zero (`if override.X != ""`).
  A project that sets `Model` but leaves `Permissions` empty does **not** wholesale-override
  User — it takes its `Model` and falls through to User for `Permissions`. An unset project
  field falls through to User before falling through to built-in defaults; the role override
  then overlays on top, unchanged.
- **No field-level explicit-clear is supported in this first layer.** Because
  `AgentConfig` is a value struct with no per-field presence bit, "is this field set?"
  is answered only by its zero value — there is no way to say "drop just the User model,
  keep the rest." Clearing the _project_ blob wholesale via `SetConfig` returns that
  project to inheriting User; it does not clear the User defaults themselves.

Storage, endpoint, and the merge are split across two PRs:

- **#2998 (this layer's backend, no merge):** new table, one row, `AgentConfig` JSON blob,
  migrated via goose; new `/api/v1/user-config` (GET/PUT); `ao user-config get/set`. The
  layer is stored and editable but has **no effect on workers yet**.
- **#2999 (merge layer, after #2848):** `effectiveAgentConfig` resolves
  project-over-User before role-over-project. Existing project blobs are untouched.
- **No UI in #2998.** The desktop form (`ProjectSettingsForm`) is unchanged; a User
  settings surface is a follow-up.

Backward compatibility is by construction: a missing User row decodes to a zero
`AgentConfig`, so existing projects and the 8k current users resolve to **exactly today's
behavior** until a User config is set.

Security (ties to #2951): when `AgentConfig` later gains an `env` field (it does not carry
one today — that is #2848), a User `env` value would reach **every** worker across **every**
project, so the layer must accept only explicitly-set typed values and must **never** copy
or persist `os.Environ()`. The User layer is opt-in typed config, not a reflection of the
host environment. Until #2848 lands this is vacuous; it becomes a real constraint at that
rebase, which is why the invariant is recorded now.

## Consequences

- A user gains one place to set a cross-project agent profile, removing per-project
  duplication. Projects remain free to override any field.
- Absent-vs-empty is deliberately **not** solved per-field. Because `AgentConfig` is a
  value struct with no per-field presence bit, clearing a single User field from the
  project layer is impossible; only the zero value can express "inherit." Clearing the
  project blob wholesale via `SetConfig` returns that project to inheriting User. If
  fine-grained clear becomes necessary later, the established precedent is a pointer for
  the field that needs a third state (no such field exists in `AgentConfig` today);
  promote fields to pointers one at a time as the need arises, without reworking the layer.
- This ADR records two distinct principles so the choice is visible to reviewers: the
  User layer **stores wholesale** (one `PUT` replaces the whole blob) and **resolves
  field-by-field** (reusing the project layer's zero-value-means-inherit overlay). The
  older code-comment-only rule for project config is unchanged.
- The first PR (#2998) is backend-only (storage + endpoint + cli, no merge) to keep the
  change narrow and reviewable; the merge layer (#2999) lands only after #2848. UI,
  additional scopes (Local, Managed), `ProfileSource` across layers, and Codex-style
  profile presets are follow-up PRs that build on this foundation.
- For the merge layer (#2999), `buildSystemPrompt` **does exist** (`manager.go`) and
  already reads project config; Phase 2 must decide whether User scope affects only
  `AgentConfig` fields or also flows into the prompt. The `mergeEnv` helper does not
  exist under that name on `main` (env is assembled in `runtimeEnv` /
  `augmentAgentRuntimeEnv`); it is introduced by #2848. Both are Phase-2 concerns and
  do not touch #2998.
