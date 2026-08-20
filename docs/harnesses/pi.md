# Pi

AO supports Pi in TUI mode through the `pi` executable. AO contains an
unregistered Chat binding for the independently installed
[`@victor-software-house/pi-acp`](https://github.com/victor-software-house/pi-acp)
distribution, but current pi-acp releases do not provide a permission boundary,
so AO neither advertises Pi as Chat-capable nor admits mutating Pi Chat sessions
until that gap is closed.

## Chat prerequisites and install policy

- AO's tested minimum is `@victor-software-house/pi-acp` 0.17.1. That release
  embeds `@earendil-works/pi-coding-agent` 0.75.3 or newer and requires Bun 1.3
  or newer.
- AO resolves `pi-acp` from `PATH` and common user-level binary locations. It
  launches `pi-acp` with no arguments over stdio.
- AO never invokes `npx`, `npm install`, `bun install`, or another download
  fallback. Install and update pi-acp separately before selecting Chat mode.
- Pi provider credentials and configuration remain in Pi's normal agent
  directory (`PI_CODING_AGENT_DIR`, or `~/.pi/agent`). AO passes the project
  environment through unchanged and uses the existing Pi adapter's auth probe.

pi-acp 0.17 runs a background daemon. AO assigns each AO session a private
socket directory under `AO_DATA_DIR`, preventing the daemon started for one
project from retaining another project's provider environment. Pi's durable
conversation files remain in Pi's own session directory.

## Protocol mapping

| Surface | AO behavior |
| --- | --- |
| Text and thinking | ACP message/thought chunks stream into Chat events. |
| Tools | ACP tool calls and updates map to tool events; edit/write structured diffs are preserved. |
| Approvals | Unsupported. Pi has no permission popups and pi-acp emits no ACP permission requests, so AO reports this capability as false. The production capability floor therefore refuses mutating Pi Chat sessions rather than silently ignoring the selected permission mode. |
| Images and attachments | Images are sent as native ACP image blocks. Text resources are sent as embedded context. Audio is not supported by pi-acp. |
| Load/resume | AO stores the pi-acp session id and uses ACP `session/load`, including structured history replay. |
| Models | pi-acp's live `model` config option drives AO's model selector; `thought_level` carries effort. |
| MCP | pi-acp 0.17.1 accepts MCP fields but does not connect them to Pi. AO therefore does not claim MCP support; a session configured with per-session MCP servers is rejected during ACP setup instead of silently ignoring them. |

For project instructions, AO uses pi-acp's resource-composition metadata to
append the same generated AO standing prompt passed to `pi
--append-system-prompt`, while retaining Pi's ordinary project `AGENTS.md`,
skills, prompts, extensions, provider config, and auth directory.

When Pi Chat can be safely admitted, it will initially remain Chat-only. AO does
not offer TUI/Chat handoff until the `pi` terminal and pi-acp can prove that
their continuation ids refer to the same durable conversation.
