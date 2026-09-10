# ao session

Manage agent sessions: list, inspect, rename, kill, restore, clean up, and claim PRs.

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

Relaunch a terminated session.

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

### ao session cleanup

Clean up terminated sessions by reclaiming eligible workspaces. Dirty worktrees are skipped by the daemon.

**Syntax:**
```
ao session cleanup [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
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
