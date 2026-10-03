# OpenCode 2 Harness Design

## Intent and scope

Add the official OpenCode 2 CLI as a separately selectable AO harness named `opencode-v2`. A user can select it for worker or orchestrator sessions and use both TUI and Chat modes. AO must preserve the existing `opencode` (OpenCode 1) harness and the major version of every saved session through restore. The OpenCode 2 reviewer is separately selectable and must enforce AO's read-only review policy. AO does not bundle OpenCode or migrate the user's OpenCode configuration or conversations.

This design assumes the official OpenCode 2 CLI, which uses the same `opencode` executable name as OpenCode 1. It does not assume simultaneous default installation of both versions. Users who keep two installations can put either binary on `PATH`; AO selects only the major version that matches the chosen harness.

## Why a separate harness

OpenCode 2 retains the `opencode` command but changes the plugin API and server API. OpenCode's migration guide says OpenCode 1 and 2 are not installed side by side by default. AO's existing TUI activity plugin is an OpenCode 1 plugin, so treating version 2 as an ordinary update to the current adapter would risk losing native session identity, activity, and permission signals. Separate identities also keep durable AO sessions and reviewer records explicit about which native major version created them.

Alternatives considered:

1. **Separate `opencode-v2` harness (chosen):** preserves historical v1 identity and makes version mismatch a clear launch/restore error. It requires v2-specific adapter, plugin, and catalog entries.
2. **Version-aware existing `opencode` harness:** a smaller selector change, but a saved session could silently switch runtime and plugin contracts when the user's binary is upgraded.
3. **Treat v2 as Chat-only ACP:** avoids porting the activity plugin but does not meet AO's existing TUI worker capability or the requested harness parity.

## Architecture and identity

Add `opencode-v2` to `domain.AgentHarness`, the agent registry, supported values in API DTOs, storage check constraints through a new migration, CLI help/doctor, model catalog, frontend labels/icons/selectors, and the `using-ao` harness list. Reuse OpenCode branding where practical while displaying “OpenCode 2” distinctly. Add an independent `opencode-v2` reviewer harness identity; do not reuse the v1 reviewer identity for a v2 process.

Both harnesses resolve the user's `opencode` executable through the existing OpenCode binary search. Before launching or restoring either one, run a bounded `--version` probe and require major 1 or major 2 respectively. A missing binary and a wrong-major binary have distinct actionable errors. The resolved path and version should remain consistent within a launch attempt so an intervening `PATH` change cannot select a different executable. AO never rewrites `opencode` paths or automatically replaces one major version with another. Add a distinct `opencode-v2` install target using official v2 package/installer choices, with an explicit notice that a default v2 install replaces v1; it must not claim both harnesses are installed from one binary.

## TUI worker lifecycle

Create a v2 agent adapter behind the existing `ports.Agent` boundary. It starts the user's `opencode` v2 TUI in AO's worktree, passes the model and initial prompt using v2-supported arguments, and restores by the v2 native session ID. V2 starts a private server for each AO TUI process (`--standalone`) so its environment and workspace-local AO plugin are scoped to that session rather than an unrelated shared background service. Launch and restore use the same AO standing instructions and permission modes, expressed in the v2 config vocabulary (`agents`, `system`, ordered `permissions`). The adapter must verify actual v2 precedence for AO's per-session overlay and preserve user configuration. A leading-dash prompt must remain data, not become a CLI option.

Port AO's managed activity plugin to the `@opencode/plugin` v2 entrypoint and hooks/events. It reports the same normalized AO signals: native session start/ID, accepted user prompt, active tool work, permission blocking/resolution, and turn stop. Each report carries `AO_RUNTIME_LAUNCH_ID` and the native session ID to preserve AO's generation fencing. Reports stay best effort, bounded, and ordered, and cannot crash OpenCode if `ao hooks` is missing or fails. Install the v2 plugin and `using-ao` skill only in AO-managed workspace paths, with ownership markers and cleanup guards equivalent to the v1 adapter. Avoid overwriting a user's plugin at the same path. Because v1 and v2 discover the same `.opencode/plugins/` directory, a version-specific install removes the other version's AO-owned plugin only after checking its marker; it never deletes a user file. The implementation must prove that neither major attempts to run an incompatible plugin.

## Chat and reviewer

OpenCode 2 exposes `opencode acp` over stdio with private server ownership. Register a v2 Chat driver using AO's existing ACP transport and detached host. It uses the v2 agent/config vocabulary for standing instructions and permission modes; validates model IDs and forwards model/effort/mode settings against the v2 ACP options. AO's existing approval policy remains the boundary: default asks, accept-edits and auto answer only the intended requests, and bypass requires an explicit session choice. A saved v1 ACP conversation cannot be loaded through the v2 driver, or vice versa.

The v2 reviewer requires a separately verified read-only permission policy. Map v1 `bash`/`permission` rules to v2 `shell`/ordered `permissions`, allow only inspection and AO's review submission path, and prove edit/shell attempts outside that policy are denied. Reviewer restoration uses its v2 native session and re-applies the same policy. Reviewer registration waits until the policy tests pass.

## Errors and compatibility

Version mismatch fails before creating a runtime or changing session state. Missing native session metadata follows AO's established fresh-launch fallback only when it is safe for the requested action; a present v1 native ID is never passed to v2, and a present v2 ID is never passed to v1. Failed/unknown probes do not mark an existing session dead. API error envelopes and request IDs remain unchanged. The primary AO HTTP listener remains loopback-only, and OpenCode's private ACP/TUI servers do not add an AO network-facing bind.

## Verification

Unit tests cover version probing, wrong-major errors, launch/restore argv, v2 config and permission overlays, hook install/uninstall ownership, activity event mapping, ACP options, and reviewer policy. Registry/API/storage tests cover the new identity and round-trip persistence. Run focused Go tests first, then the repository's applicable complete CI commands, generated API/sqlc drift checks, and frontend typecheck/build. An opt-in live test with an installed OpenCode 2 binary verifies TUI start, prompt, idle/permission transitions, restore, ACP initialize/prompt/resume, and reviewer denial behavior if reviewer support is included. If no v2 binary is available locally, report that exact live-validation gap and use CI or a dedicated environment before claiming live integration.

## Upstream references

- [OpenCode 2 installation and executable](https://opencode.ai/v2/docs)
- [Migration from OpenCode 1](https://opencode.ai/v2/docs/migrate-v1/)
- [OpenCode 2 CLI and private server option](https://opencode.ai/v2/docs/cli)
- [OpenCode 2 ACP](https://opencode.ai/v2/docs/cli/acp/)
- [OpenCode 2 plugin API](https://opencode.ai/v2/docs/build/plugins)
