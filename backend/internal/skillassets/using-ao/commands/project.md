# ao project

Manage projects: register repos, inspect, configure per-project settings, and remove.

## Syntax

```
ao project <subcommand> [args] [flags]
```

## Subcommands

---

### ao project add

Register a local git repo as a project so sessions can be spawned in it. The path must be an existing git repository on disk. With `--as-workspace`, the path may be a parent folder containing direct child git repositories; AO initializes/adopts the parent as the root repo and gitignores children.

**Syntax:**
```
ao project add [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--as-workspace` | Register a parent folder as a workspace project (root-as-repo plus direct child repos) | - |
| `--id string` | Project id | Derived by the daemon from the path |
| `--name string` | Display name | - |
| `--orchestrator-agent string` | Default orchestrator session agent | - |
| `--path string` | Absolute path to the local git repo | Required |
| `--worker-agent string` | Default worker session agent | - |

**Examples:**

```bash
# Register a repo as a project
ao project add --path /path/to/agent-orchestrator --name "agent-orchestrator"
```

```bash
# Register a workspace (parent folder containing multiple repos)
ao project add --path /path/to/side-quests --as-workspace --name "side-quests"
```

---

### ao project ls

List registered projects. Aliases: `ls`, `list`.

**Syntax:**
```
ao project ls [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output projects as JSON | - |

**Examples:**

```bash
# List all registered projects
ao project ls
```

---

### ao project get

Fetch one registered project.

**Syntax:**
```
ao project get <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output project as JSON | - |

**Examples:**

```bash
# Get details for the agent-orchestrator project
ao project get agent-orchestrator
```

---

### ao project rm

Remove a registered project. Aliases: `rm`, `remove`, `delete`.

**Syntax:**
```
ao project rm <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output removal result as JSON | - |
| `-y, --yes` | Skip confirmation prompt | - |

**Examples:**

```bash
# Remove a project (with confirmation)
ao project rm agent-orchestrator
```

```bash
# Remove without prompt
ao project rm agent-orchestrator -y
```

---

### ao project set-config

Replace a project's saved configuration. AO reads these settings when it spawns a session. Use flags for common fields or `--config-json` for the full object. `--clear` removes all configuration.

**Warning:** This command replaces the whole config, even when you use focused flags. The CLI silently ignores unknown keys in `--config-json`, so a typo can leave the replacement empty. Its config mirror also omits `effort`; `ao project get --json` cannot show it, and a CLI configuration replacement drops existing effort values. Use Project Settings in the desktop app or the daemon API when you need to preserve or change effort. See the [full-config warning](https://docs.orchestrator.inc/configuration/projects/#update-from-the-cli) before updating project settings from the CLI.

**Syntax:**
```
ao project set-config <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--agent-rules string` | Project-specific standing instructions appended to worker session prompts | - |
| `--agent-rules-file string` | Repo-relative file containing project-specific worker standing instructions | - |
| `--canonical-repo-url string` | Explicit upstream HTTPS repository URL used for PR claims (same provider, host, and port as `origin`) | - |
| `--clear` | Clear all config | - |
| `--config-json string` | Full config as a JSON object (overrides field flags) | - |
| `--default-branch string` | Base branch new session worktrees are created from | - |
| `--env stringArray` | Env var `KEY=VALUE` forwarded into sessions (repeatable) | - |
| `--json` | Output the updated project as JSON | - |
| `--model string` | Agent model override (e.g. `claude-opus-4-5`) | - |
| `--orchestrator-agent string` | Harness override for orchestrator sessions | - |
| `--orchestrator-rules string` | Project-specific standing instructions appended to orchestrator session prompts | - |
| `--permission string` | Permission mode: `default`, `accept-edits`, `auto`, `bypass-permissions` | - |
| `--post-create stringArray` | Command to run after workspace creation (repeatable) | - |
| `--session-prefix string` | Displayed session-id prefix | - |
| `--symlink stringArray` | Repo-relative path to symlink into workspaces (repeatable) | - |
| `--tracker-assignee string` | Issue assignee required for tracker intake eligibility | - |
| `--tracker-intake` | Opt this project into GitHub or GitLab issue intake; inert unless the daemon also has `AO_TRACKER_INTAKE=on` | Disabled |
| `--tracker-repo string` | Provider-native issue repository (`owner/repo` or `group/subgroup/repo`); defaults to the Git origin | - |
| `--reviewer stringArray` | Reviewer harness for worker PRs (repeatable) | - |
| `--worker-agent string` | Harness override for worker sessions | - |

Use `--config-json` for fields without a dedicated flag, including
`trackerIntake.provider`, `autoReview`, `containerReap.disabled`,
`agentConfig.mode`, `worker.agentConfig.mode`, and
`orchestrator.agentConfig.mode`. The full-config replacement warning above
still applies.

**Examples:**

```bash
# Set default branch and model for a project
ao project set-config agent-orchestrator --default-branch main --model claude-opus-4-5
```

```bash
# Replace the complete config; include every setting you want to keep
ao project set-config agent-orchestrator --config-json \
  '{"defaultBranch":"main","agentConfig":{"model":"claude-opus-4-5"}}'
```

```bash
# Set an env var and a post-create command
ao project set-config agent-orchestrator --env "NODE_ENV=development" --post-create "npm install"
```

```bash
# Set worker and orchestrator standing rules
ao project set-config agent-orchestrator --agent-rules "Run focused tests before reporting done." --orchestrator-rules "Delegate implementation work to worker sessions."
```

```bash
# Load worker rules from a repo-relative file
ao project set-config agent-orchestrator --agent-rules-file docs/ao-worker-rules.md
```

```bash
# Enable assignee-scoped issue intake (requires AO_TRACKER_INTAKE=on)
ao project set-config agent-orchestrator --tracker-intake \
  --tracker-repo owner/repo --tracker-assignee alice
```
