# Plandex harness conformance

Status: **blocked and intentionally unregistered**.

AO inspected Plandex CLI `2.2.1`, tag `cli/v2.2.1`, commit
`df17a187974c3795c3f1d2ea47bbacbdb675dde5`. This is the newest released CLI
tag observed on 2026-09-22. The evidence below comes from that tag's CLI source,
built-in help, and documentation. The live probe used the official
`plandex_2.2.1_darwin_arm64.tar.gz` release asset with SHA-256
`1fd4ee494e5ad9bfd19d1cea6e7cbce971500f7bec93aef826f6e09dde9e70e5`.

## Upstream contract

| Concern | Plandex 2.2.1 evidence | AO result |
| --- | --- | --- |
| Binary and platforms | `plandex version`; `plandex` executable with optional `pdx` symlink. Official binaries support macOS, Linux, and FreeBSD; Windows is WSL-only. | Sufficient for discovery, but AO must not advertise native Windows support. |
| Install | The official quick install pipes a remote shell script; manual release downloads and source builds are documented. | AO must not run the remote script silently. No installer is registered. |
| Authentication | `~/.plandex-home-v2/auth.json` is read on startup. Missing auth starts an interactive sign-in flow; no documented local `auth status` or `whoami` command exists. | File presence could mean only configured, not authorized. Readiness is unproven. |
| Initial task | The interactive REPL accepts terminal input only after auth, project resolution, optional plan creation, settings/model checks, and prompt initialization. `plandex tell [prompt]` is a separate command, not a documented interactive REPL launch channel. | No race-free interactive initial-task mechanism. |
| Standing instructions | `plandex load --note` adds visible, plan-persistent context. There is no documented per-process hidden system/instruction flag or overlay. | Hard blocker. AO must not turn private standing configuration into a user message or persistent user plan context. |
| Hooks and activity | The CLI exposes no documented per-process lifecycle hook configuration. Server-side hooks are implementation/plugin internals, not a user CLI observer contract. | Hard blocker. AO cannot observe active, blocked, settled, and exited states without changing provider behavior. |
| Native identity | A durable plan ID exists on the server, but the REPL does not emit it through a machine-readable startup event. The local current plan is selected through account-keyed files. | Hard blocker. AO cannot capture a reliable native ID. |
| Restore | `plandex cd` selects a plan by mutable list index or name, then mutates `~/.plandex-home-v2/<project>/current-plans-v2.json`. Starting the REPL resumes whichever plan is currently selected. There is no exact `plandex resume <plan-id>` form. | Hard blocker. Selection is not deterministic and concurrent AO sessions could race through shared current-plan state. |
| Permissions | `--no-auto`, `--basic`, `--plus`, `--semi`, and `--full` configure workflow automation. They are not documented equivalents for AO's manual, accept-edits, auto, and bypass permission modes. | Hard blocker. AO cannot truthfully map permission modes. |
| Cancellation | Ctrl-C handling and `plandex stop [stream-id-or-plan] [branch]` exist, but the stop command first resolves mutable project/current-plan state and does not expose a stable AO-owned session handle. | Exact bounded cancellation is unproven. |
| Models | REPL flags select model packs. Detailed model/provider state remains plan/server configuration. | Could become a mode catalog after the lifecycle gates pass. |
| Chat protocol | No ACP or other AO-compatible structured session protocol is documented by the CLI. | Chat and TUI/Chat handoff are out of scope. |

Plandex also stores authentication, account selection, project identity, current
plan selection, and REPL state below `~/.plandex-home-v2`. AO must not replace
`HOME` to isolate it: doing so would hide the user's authentication and session
state or require copying credentials. The workspace-local `.plandex-v2`
directory is only part of the selection mechanism and is not an independent
session store.

## Implemented gate

`backend/internal/adapters/agent/plandex` contains a fail-closed contract
validator and an opt-in executable probe. The normal unit suite asserts that the
observed 2.2.1 contract remains rejected. The live probe runs only when explicitly
given a pinned binary and uses temporary HOME and workspace directories:

```bash
cd backend
AO_PLANDEX_CONFORMANCE_BINARY=/absolute/path/to/plandex \
  go test ./internal/adapters/agent/plandex \
  -run TestPlandexUpstreamConformance -count=1 -v
```

Against 2.2.1 the live command is expected to fail and list the missing gates.
That failure is the registration stop condition, not a product test regression.

## Registration boundary

Until a released Plandex CLI passes the live behavioral contract, do not add
`plandex` to `domain.AllHarnesses`, the production agent registry, SQLite CHECK
constraints, HTTP DTO enums, generated API artifacts, product UI identities, or
reviewer support. A future implementation must first prove:

1. a hidden per-process standing-instruction channel that preserves Plandex's
   defaults and user configuration;
2. isolated, observation-only hooks that preserve any user hooks;
3. startup emission of a durable plan/session ID plus exact restore by that ID;
4. truthful permission mappings and bounded cancellation; and
5. a non-interactive auth probe that distinguishes configured from authorized.

After those gates pass, implement and test the TUI adapter before production
registration. Provider-internal orchestration remains opaque activity inside
one AO session; reviewer support and structured Chat require separate contracts.
