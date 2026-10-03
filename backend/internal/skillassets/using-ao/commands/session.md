# ao session

Manage agent sessions: list, inspect, rename, kill, restore, exit or resume an
agent, switch harnesses, clean up, and claim PRs.

## In-app session links

When referring a user or another agent to a session in AO Chat or the AO
terminal, emit this canonical link:

```text
ao://sessions/{project-id}/{session-id}
```

- Use stable IDs returned by `ao project ls` and `ao session ls`, or the current
  session's `AO_PROJECT_ID` and `AO_SESSION_ID`. Never use display names as
  identity; renaming a session must not change its link.
- Treat the project ID and session ID as separate URL path segments. Encode
  either ID with URL percent encoding if it contains characters that are not
  safe in one segment.
- Emit only the navigation route above. Do not append query strings, fragments,
  action names, credentials, authorities, or extra path segments.
- The link is handled only inside a currently running AO desktop app. It is not
  an operating-system protocol link and must not be described as supporting
  cold launch or navigation from external applications.
- A session link navigates; it never sends a message, executes a command, or
  performs another action.

Example for project `mercury` and session `mer-3`:

```text
ao://sessions/mercury/mer-3
```

## Syntax

```
ao session <subcommand> [args] [flags]
```

## Subcommands

---

### ao session ls

List sessions.

**Syntax:**
```
ao session ls [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `-a, --all` | Include orchestrator sessions | - |
| `--include-terminated` | Include terminated sessions | - |
| `--json` | Output as JSON | - |
| `-p, --project string` | Filter by project ID | - |

**Examples:**

```bash
# List all active worker sessions
ao session ls
```

```bash
# List all sessions including terminated, scoped to one project
ao session ls --include-terminated -p agent-orchestrator
```

---

### ao session get

Fetch one session.

**Syntax:**
```
ao session get <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output as JSON | - |
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
# Get details for session mer-3
ao session get mer-3
```

```bash
# Get session details as JSON
ao session get mer-3 --json
```

---

### ao session kill

Terminate a session.

**Syntax:**
```
ao session kill <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
# Kill session mer-3
ao session kill mer-3
```

---

### ao session rename

Rename a session.

**Syntax:**
```
ao session rename <id> <name> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
# Rename session mer-3 to a new display name
ao session rename mer-3 "fix-auth-bug"
```

---

### ao session restore

Restore a terminated session or resume an exited agent.

**Syntax:**
```
ao session restore <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
# Restore a terminated session
ao session restore mer-3
```

---

### ao session exit-agent

Exit an agent without terminating its AO session. The session can later be
resumed in place.

**Syntax:**
```
ao session exit-agent <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output as JSON | - |
| `-p, --project string` | Project id to scope the lookup | - |

---

### ao session resume-agent

Resume an exited agent in its existing AO session.

**Syntax:**
```
ao session resume-agent <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output as JSON | - |
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
ao session exit-agent mer-3
ao session resume-agent mer-3
```

---

### ao session switch-agent

Switch a running session to another installed agent harness. This preserves the
AO session while provisioning the target harness.

**Syntax:**
```
ao session switch-agent <id> <target-harness> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--idempotency-key string` | Reuse a prior identical switch request safely | - |
| `--json` | Output the agent switch as JSON | - |

**Examples:**

```bash
ao session switch-agent mer-3 codex
```

---

### ao session agent-switch ls

List agent-harness switches recorded for a session. Alias: `agent-switches`.

**Syntax:**
```
ao session agent-switch ls <session-id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output agent switches as JSON | - |

**Examples:**

```bash
ao session agent-switch ls mer-3
```

---

### ao session cleanup

Clean up terminated sessions by reclaiming eligible workspaces. Dirty worktrees are skipped by the daemon.

**Syntax:**
```
ao session cleanup [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--dry-run` | Preview which sessions would be cleaned without removing anything | - |
| `-p, --project string` | Filter by project ID | - |
| `-y, --yes` | Skip confirmation prompt | - |

**Examples:**

```bash
# Clean up all terminated sessions (skip prompt)
ao session cleanup -y
```

```bash
# Clean up terminated sessions for one project
ao session cleanup -p agent-orchestrator
```

```bash
# Preview eligible cleanup candidates
ao session cleanup --dry-run
```

---

### ao session claim-pr

Attach an existing PR to the current AO session, or target another session explicitly.

**Syntax:**
```
ao session claim-pr <pr-ref> [flags]
ao session claim-pr <session-id> <pr-ref> [flags]
```

With one positional argument, `AO_SESSION_ID` supplies the session. This is the preferred form inside a worker. Pass both arguments from an orchestrator or external shell when targeting another session.

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output as JSON | - |
| `--no-takeover` | Refuse if another active session owns the PR | - |
| `-p, --project string` | Project id to scope the lookup | - |

**Examples:**

```bash
# Attach PR 88 to the current worker session
ao session claim-pr 88
```

```bash
# Attach PR 88 to session mer-3 explicitly
ao session claim-pr mer-3 88
```

```bash
# Claim PR 88 for the current worker but refuse if another session owns it
ao session claim-pr 88 --no-takeover
```
