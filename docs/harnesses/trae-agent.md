# Trae Agent conformance status

ByteDance Trae Agent is **not registered as an Agent Orchestrator harness**.
AO contains only a pre-registration conformance gate for a user-supplied
`trae-cli` executable. There is no domain value, registry constructor, SQLite
migration, API enum, installer, product picker, avatar, Chat driver, interface
handoff, or reviewer entry for Trae Agent.

AO's pre-existing `trae` editor-handoff label is unrelated to an agent harness.
The registration test guards `trae-agent`, `trae-cli`, and `trae` so that
package, executable, or editor naming cannot accidentally be mistaken for
worker, Chat, or reviewer support.

## Pinned upstream evidence

The audit was performed on 2026-09-22 against the official
[`bytedance/trae-agent`](https://github.com/bytedance/trae-agent) repository:

| Evidence | Pinned value |
| --- | --- |
| Declared source version | `0.1.0` |
| Commit | [`e839e559ac61bdd0e057c375dd1dee391fee797d`](https://github.com/bytedance/trae-agent/commit/e839e559ac61bdd0e057c375dd1dee391fee797d) |
| Git tree | `fceea1cae3ddf5fcc29649db47449c54e011844e` |
| Commit signature | GitHub API reported `verified: true`, reason `valid` |
| `uv.lock` SHA-256 | `467d41bb4903b96dba6a1b7e44cc176301d813c7eb4f35a958cb3c7c38026689` |
| `pyproject.toml` SHA-256 | `1340cd50bfebb929963aaca818229ea5edc29b50386161cc56e94cd4399bd73c` |

This is an audited **source snapshot, not an upstream release**. At audit time,
the official repository had no GitHub releases and no tags, and PyPI returned
404 for both `trae-agent` and `trae-cli`. The repository declares version
`0.1.0`, but that alone does not create a released, installable artifact. Two
downloads for the exact commit produced different GitHub-generated archive
bytes during the audit, so no archive digest is presented as durable
provenance; the commit and tree are the source identity.

Upstream documents source installation with Python 3.12+, `uv sync
--all-extras`, and activation of the checkout's virtual environment. The
declared console script is `trae-cli = trae_agent.cli:main`. There is no
official platform support matrix or AO-safe package installer to register.

The pinned checkout did complete `uv sync --frozen --all-extras` locally with
CPython 3.12.13. Its generated `trae-cli` wrapper is tied to the temporary
virtual-environment path and is not a distributable artifact identity. In the
sanitized, credential-free probe, even `trae-cli --version` produced no output
within 30 seconds and was terminated. That environment-specific result is
recorded as unavailable evidence, not as a general performance claim and not as
a pass for any runtime contract.

## Contract result

| Concern | Result | Evidence and consequence |
| --- | --- | --- |
| Install and binary | **Fail** | [`README.md` lines 27-40](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/README.md#L27-L40) installs from a mutable Git checkout. [`pyproject.toml` lines 1-6](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/pyproject.toml#L1-L6) declare version 0.1.0 and [lines 44-49](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/pyproject.toml#L44-L49) declare `trae-cli`, but no tagged/released artifact exists. AO must not offer an installer. |
| Authentication | **Fail** | Authentication is provider API-key configuration from [YAML or environment](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/README.md#L42-L109) or `--api-key`; there is no login/logout or verified local auth-status command. [`show-config`](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/cli.py#L620-L704) reports configured values and partially renders the key, so it is not a safe authorization probe. |
| Interactive launch | **Present** | [`trae-cli interactive`](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/cli.py#L414-L506) exists and retains one in-memory Agent instance for multiple turns. This does not imply resumability. |
| Initial prompt | **Fail** | [`run TASK`](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/cli.py#L126-L143) accepts an initial task but is a one-shot command. `interactive` has no message/prompt or working-directory option and [first asks for a task, then separately asks for the working directory](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/cli.py#L521-L547), so AO has no documented, race-free initial-prompt mechanism for a persistent TUI. |
| Standing instructions | **Fail** | [`TraeAgent.get_system_prompt`](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/agent/trae_agent.py#L172-L175) returns one built-in constant. There is no per-process hidden system-prompt file or append-only overlay. AO could only expose its instructions as user text or modify upstream/user state, both prohibited. |
| Permission modes | **Fail** | The CLI exposes no approval or permission mode. [Model tool calls are dispatched directly](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/agent/base_agent.py#L314-L350) by the tool executor, so AO cannot truthfully map manual, accept-edits, auto, and bypass behavior. |
| Hooks and lifecycle | **Fail** | There is no hook input or structured lifecycle stream for submitted, active, blocked, settled, session-start, or exited events. [Trajectory JSON defaults under the working directory](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/utils/trajectory_recorder.py#L20-L37) and is neither additive hook configuration nor a live lifecycle protocol. |
| Native identity | **Fail** | No provider-native conversation/session ID is exposed or persisted. The word “session” in the bash tool refers only to an internal shell process. |
| Exact resume | **Fail** | Neither the root command nor `interactive` accepts a resume/session identifier, and no saved conversation is loaded. Exact restore and daemon-restart continuity are unavailable. |
| Cancellation | **Fail** | In interactive mode, [`KeyboardInterrupt` is caught](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/cli.py#L588-L592) and merely tells the user to type `exit` or `quit`; upstream publishes no bounded turn-cancellation contract. [The bash tool terminates only its shell process](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/tools/bash_tool.py#L63-L85) and can return after 5-second and 2-second cleanup timeouts without proving descendant cleanup. AO therefore cannot claim bounded cancellation. |
| Models | **Partial** | `--provider`, `--model`, `--model-base-url`, `--api-key`, and `--config-file` are available. Model IDs are free-form and there is no discoverable catalog or authorization check. This isolated capability does not unblock registration. |
| ACP | **Fail** | Trae Agent is an [MCP client](https://github.com/bytedance/trae-agent/blob/e839e559ac61bdd0e057c375dd1dee391fee797d/trae_agent/utils/mcp_client.py#L1-L103) for configured tool servers. It exposes no Agent Client Protocol server/stdio mode, `initialize`, `session/new`, `session/load`, replay, permission, cancellation, restart-recovery, or capability contract. MCP support is not ACP support. |

The TUI gate fails independently on release provenance, executable identity,
profile preservation, verified authentication, hidden standing instructions,
additive hooks, interactive initial delivery, permission modes, lifecycle
events, native identity, exact resume, and bounded cancellation. The ACP gate
fails independently on the complete structured protocol contract. These are
hard blockers, not follow-up polish.

## Running the opt-in executable probe

The ordinary package suite is credential-free and skips the real executable:

```bash
cd backend
go test ./internal/adapters/agent/traeagent -count=1
```

To inspect an explicitly installed source build, provide its absolute path and
the SHA-256 of that exact wrapper:

```bash
cd backend
AO_TRAE_CONFORMANCE_BINARY=/absolute/path/to/trae-cli \
AO_TRAE_CONFORMANCE_SHA256=<sha256-of-that-executable> \
  go test ./internal/adapters/agent/traeagent \
  -run TestTraeUpstreamConformance -count=1 -v
```

The probe runs only `--version` and help commands with disposable `HOME`, XDG,
AO-data, and workspace directories. It inherits a small non-secret environment
allowlist and strips provider keys, loader overrides, and user configuration.
It snapshots the disposable roots and fails if even those read-only commands
write files. CLI syntax is logged, but only the version is admitted into the
behavioral contract. The final subtests intentionally fail until real evidence
proves all registration requirements for an official release.

## Required upstream changes before another audit

A future, pinned release must provide and document all of the following before
AO may implement or register a TUI harness:

- an immutable release artifact and supported-platform matrix;
- a per-process hidden, append-only standing-instruction input that preserves
  the built-in prompt and ordinary user/project configuration;
- a per-process, additive observation hook/event channel that preserves all
  user hooks and reports lifecycle transitions plus one durable native ID;
- race-free initial task delivery to the interactive process;
- explicit, truthful permission modes that AO can map without weakening them;
- exact resume by the captured native ID, with a typed failure for missing or
  mismatched history;
- bounded turn cancellation and bounded process-tree cleanup; and
- a local auth-status probe that distinguishes configured credentials from
  verified authorization without printing secrets.

ACP Chat requires its own real-executable conformance pass for initialize,
new/load, stable identity, replay, streaming, permissions, cancellation,
restart/reconnect, missing-history and workspace guards, model configuration,
and truthful optional-capability claims. TUI/Chat handoff additionally requires
proof that both interfaces share identity and history. Reviewer support remains
out of scope until its separate isolation and cancellation contract is proven.

## Deliberate exclusions

Because the required contracts fail, this package does not implement launch or
restore commands, hooks, activity parsing, authentication readiness,
installation, domain/API/database/UI registration, Chat, handoff, reviewer
support, or provider-internal subagent exposure. It also never replaces `HOME`
or another provider profile in production merely to manufacture isolation.

## Upstream references

- [Repository](https://github.com/bytedance/trae-agent)
- [Pinned commit](https://github.com/bytedance/trae-agent/commit/e839e559ac61bdd0e057c375dd1dee391fee797d)
- [GitHub releases API](https://api.github.com/repos/bytedance/trae-agent/releases)
- [GitHub tags API](https://api.github.com/repos/bytedance/trae-agent/tags)
- [PyPI `trae-agent` project API](https://pypi.org/pypi/trae-agent/json)
- [PyPI `trae-cli` project API](https://pypi.org/pypi/trae-cli/json)
