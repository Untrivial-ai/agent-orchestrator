# ACP Chat bindings and the TUI-only harness matrix

Chat is opt-in per harness. The registry
(`backend/internal/adapters/chatdriver/registry/registry.go`) is the whole
capability gate: a harness with no driver returns `ErrChatUnsupported`, and the
desktop never offers the TUI ⇄ Chat toggle for it. This page records each
binding's launch shape, how AO maps its permission and standing-instruction
policy, and why the remaining harnesses are still TUI-only.

## Shipped bindings

Every binding below launches the executable resolved by that harness's existing
agent plugin. AO never downloads, packages, or substitutes the provider CLI.

| Harness | Launch | Model | Permissions | Standing instructions |
| --- | --- | --- | --- | --- |
| Auggie | `auggie --acp` | `--model` at launch and `session/set_model` | ACP requests; `accept-edits`/`bypass` auto-resolve | session-private `--rules` file |
| Autohand | `autohand-acp` | session-advertised model options | `AUTOHAND_PERMISSION_MODE=external`; `accept-edits`/`auto`/`bypass` auto-resolve | not injectable |
| Cline | `cline --acp` | `CLINE_MODEL` env and `session/set_config_option` | `--auto-approve true` for `auto`/`bypass`; `accept-edits` auto-resolves edit tools | not injectable (ACP ignores `--system`) |
| Goose | `goose acp` | session-advertised model options | `GOOSE_MODE` env (`smart_approve`/`auto`) | not injectable |
| Kilo Code | `kilocode acp` | `session/set_config_option` (`model`, `effort`) | `KILO_CONFIG_CONTENT` permission map | `KILO_CONFIG_CONTENT` generated primary agent |
| Kiro | `kiro-cli acp --agent ao` | `session/set_model` | ACP requests; `accept-edits`/`auto`/`bypass` auto-resolve | workspace-local `ao` custom agent |
| Prime Agent | `prime-agent --mode acp` | not selectable: no config options, no `session/set_model` | unsupported: bypass-only admission | not injectable |
| Vibe | `vibe-acp` | session-advertised model options | ACP requests; `auto`/`bypass` auto-resolve, `accept-edits` prompts | not injectable |

Codex remains on its native app-server. Claude Code, Cursor, OpenCode, Droid,
Kimi, Kimchi, Pi, OMP, and Qwen keep the bindings they already shipped.

### Deliberate limitations

- **Vibe** sends no tool kind on a permission request, so AO cannot tell an
  edit from a shell command and `accept-edits` prompts exactly like `default`
  rather than auto-approving a call it cannot classify. `auto` and `bypass` are
  unaffected.
- **Cline, Goose, and Vibe** expose no launch-time standing-instruction surface
  in ACP mode, so AO's role prompt is not forwarded. Cline ignores `--system`
  once `--acp` is set; `goose acp` accepts only `--with-builtin` and
  `--enable-scheduler`; `vibe-acp` accepts only setup/harness flags. The
  provider's own project rules (for example `AGENTS.md`) still apply.
- **Prime Agent** has no ACP permission requests and no `session/load`, so AO
  reports approvals and resume unsupported. Chat admission therefore requires
  the explicit per-session bypass choice, exactly as Pi does.
- Approval changes that a binding fixes at process launch are rejected with
  `ErrACPSetterUnsupported` rather than silently ignored; the UI tells the user
  to restart Chat. Bindings that resolve approvals per ACP request instead
  (Auggie, Autohand, Kiro, Vibe) apply a mid-session change on the next tool
  call and ship no such validator. Goose compares the launched `GOOSE_MODE`
  rather than the AO mode, so switching between `auto` and `bypass` -- which
  launch identically -- does not demand a restart.

### Verification status

Every binding except Kiro was exercised against the provider's real ACP server
on macOS (arm64) on 2026-09-19; Kiro's CLI is not installable here and its row
says so. "Handshake" means `initialize` plus `session/new`
over stdio; several providers refuse `session/new` until their own `login` has
run, which confirms the launch shape without confirming session behavior.

| Binding | Version | Evidence |
| --- | --- | --- |
| Auggie | 0.18.1 | `auggie --acp` handshake: `loadSession: true`. `session/new` returns "Auggie does not currently support authenticating over ACP", so the driver `Probe` is the auth gate. |
| Autohand | adapter 0.2.1 | `autohand-acp` handshake: `loadSession: true`, session capabilities `list`/`resume`/`fork`. `AUTOHAND_PERMISSION_MODE` is present in the published adapter. `session/new` requires `autohand login`. |
| Cline | 3.0.62 | `cline --acp` handshake: `loadSession: true`. `--help` confirms `--acp` and `--auto-approve <boolean>`; `CLINE_MODEL` is present in the shipped binary. `session/new` requires login. |
| Kilo Code | 7.7.5 | `kilo acp --help` plus a full `session/new`: the session advertises selects `model`, `effort`, and `mode`, and `KILO_CONFIG_CONTENT` is present in the shipped binary. AO maps `model` and `effort`; `mode` is Kilo's agent mode, not an approval mode. `default_agent` was probed directly and is honored only for a primary agent -- see below. |
| Prime Agent | 0.7.2 | `prime-agent --mode acp` handshake: `loadSession: false`, `session/new` advertises no config options, and `session/set_config_option` and `session/set_model` both answer `-32601`. AO therefore sends no session selectors. |
| Kiro | not installed | `kiro-cli acp` is documented with `--agent <name>` only; no launch-time trust or model flag is published for the `acp` subcommand, so permissions stay on ACP requests. |
| Goose | 1.51.0 | `goose acp` handshake: `loadSession: true`. `--help` lists exactly `--with-builtin` and `--enable-scheduler`, confirming no model or system-prompt flag; `GOOSE_MODE` and `smart_approve` are present in the shipped binary. `session/new` requires `GOOSE_PROVIDER`. |
| Vibe | 2.25.5 | `vibe-acp` handshake: `loadSession: true`. `--help` lists only `--setup`/harness flags. Its `request_permission` (`vibe/acp/agent.py`) always offers `allow_once`, `allow_always`, `allow_always_permanent`, `reject_once`, but sends no tool kind -- hence the `accept-edits` limitation above. `session/new` requires a Mistral API key. |

Kilo's `default_agent` needs `mode: "primary"` on the generated agent. Probing
`session/new` with three configs shows why, since the failure is silent:

| `KILO_CONFIG_CONTENT` | advertised session mode |
| --- | --- |
| none | `code` (Kilo's default) |
| agent + `default_agent`, no `mode` | `ask` -- `default_agent` ignored |
| agent + `default_agent` + `mode: "primary"` | `ao-<session>` |

Without it the session silently runs Kilo's read-only `ask` agent and none of
AO's standing instructions apply, so `PrepareACPConfigContent` declares the
generated agent primary. The TUI path is unaffected: it selects the agent with
`--agent`, which works for any mode.

Every binding is covered by focused unit tests over launch argv, environment,
permission mapping, and turn-setting validation. Only Auggie ships an
end-to-end live test; following the existing `cursoracp`/`opencodeacp`
convention it is env-gated with `t.Skip` (`AO_LIVE_AUGGIE_ACP=1`) rather than
build-tagged, and it requires Auggie to be installed and logged in. CI never
depends on it.

## TUI-only harnesses: blocker matrix

| Harness | Structured protocol found | Evidence | Outcome |
| --- | --- | --- | --- |
| Aider | none | One-shot `--message`/`--message-file` only; no persistent agent protocol. | Blocked: no ACP or native structured control surface. |
| Amp | none | `@ampcode/cli` (renamed from `@sourcegraph/amp`); no ACP entrypoint published. | Blocked. |
| Crush | native server protocol, not ACP | `internal/server`, `internal/proto/server.go`, and a `/control` endpoint implement Crush's own client/server API. Upstream ACP is still an open request (charmbracelet/crush#2091, #990); the only ACP surface is the unofficial `crush-acp` npm adapter. | Blocked for the ACP transport, on the same rule as Muse: no first-party protocol. A bespoke driver over Crush's native protocol would be a new non-ACP transport and could not be validated here. |
| Continue | none | `cn` (`@continuedev/cli`) is a one-shot/TUI CLI; no ACP or persistent bidirectional protocol. | Blocked. |
| Devin | none | Vendor CLI exposes no local ACP or structured session protocol. | Blocked. |
| Grok | none | `superagent-ai/grok-cli` has no ACP or persistent structured protocol. | Blocked. |
| Kilo Code | ACP | `@kilocode/cli` is a fork of OpenCode with a native `acp` subcommand. | Implemented. |
| Muse | third-party, unofficial | `@bex-co/muse-code-acp` is an unofficial community adapter for Meta's `muse` CLI; Meta publishes no first-party ACP server. | Not implemented: no first-party protocol and no local binary to validate the community adapter. |
| Prime Agent | ACP | `prime-agent --mode acp`; `packages/coding-agent/src/modes/acp`. | Implemented. |
| Autohand | ACP | Vendor-published `@autohandai/autohand-acp` adapter. | Implemented. |
