# Junie integration evidence

Reviewed 17 September 2026 against the official Junie documentation, last
modified 16 September. This is development evidence, not an available AO agent.

## Admission decision

Junie remains absent from AO's agent and Chat registries, harness enum, database
constraints, setup catalogs, and product selectors. The experimental terminal
package constructs commands and isolated runtime files; passing its offline
tests is not evidence of a working authenticated Junie session.

The [hooks documentation](https://junie.jetbrains.com/docs/junie-cli-hooks.html)
still requires Early Access. The reviewed stable feed ends at `3196.5`
(`26.9.14`), while the EAP feed ends at `3294.4` (`26.9.21`). The original plan's
`3196.4` stable build is not a verified hook-capable baseline. The
[contract fixture](../../backend/internal/adapters/agent/junie/testdata/contract.json)
records both channels and the official artifact URLs and SHA-256 values.
Artifacts have not been downloaded or executed; these are published identities,
not local verification results.

## Corrections to the original plan

- An empty `--guidelines-filename` override suppresses normal project guidance.
  The runtime builder returns no guideline override when standing instructions
  are absent or blank. With AO instructions, the custom file is **exclusive**:
  it replaces Junie's normal project guideline selection. The experimental
  package does not claim additive behavior or a privileged system prompt.
  Preserving the intended project/global guidance and verifying absolute custom
  paths remain prerequisites for production.
- No `PermissionRequest` hook is installed. A synchronous hook that exits zero
  can approve an action and skip Junie's dialog. Junie documents `async: true`
  as observation-only, but a live denial/approval test on a stable release is
  still required. The adapter must not enable AO's automatic Enter nudges.
- `--prompt` is the documented persistent interactive launch form, queued until
  onboarding completes. `--task` and positional tasks are headless. Cold restore
  must explicitly use `--resume --session-id <native-id>`, never bare resume.
  Command-construction tests do not prove that resume plus a new prompt works.
- The Windows installer creates `~/.local/bin/junie.bat`; executable discovery
  must include that shim as well as PATH and the Unix `~/.local/bin/junie`.
- AO currently has no `configured` authentication status. Key presence cannot
  prove authorization, and missing environment variables cannot disprove native
  OAuth credentials. Local auth remains `unknown`; no credential store is read
  and no model request is made by readiness checks.
- Production install, setup, and model-catalog changes are deferred along with
  registration. Shipping those surfaces while registration is blocked would
  advertise an unavailable agent.
- ACP is deferred. The public
  [ACP overview](https://junie.jetbrains.com/docs/junie-cli-acp.html) documents
  `junie --acp true`, but not the durable load, history replay, or configuration
  option contract AO requires. JetBrains' own
  [Hermes client](https://github.com/JetBrains/junie/blob/1a751ed5427df267abbe3e1797f733ebaf3d5879/hermes-plugin/junie_hermes/client.py)
  uses fresh sessions and transcript replay. A fake-backed driver alone would
  not establish Junie's compatibility.

## Evidence required before registration

Use a pinned official artifact and authenticated throwaway project; record its
platform, channel, build and verified SHA-256. Never test against real AO data.

1. Verify interactive initial prompt delivery exactly once, including first-run
   authentication, project trust, and leading-dash input.
2. Verify persistent AO guidelines while preserving the intended user/project
   instruction sources. Recheck on restore and after a follow-up turn.
3. Capture a non-empty native ID from fresh `SessionStart`; cold-resume that exact
   ID and prove prior context and a resume-time prompt. Process startup alone is
   metadata, not an active turn.
4. On a stable hook release, prove an observation-only permission hook leaves the
   dialog visible and the action pending; explicit denial must prevent the
   action, and explicit approval must allow it. Recheck delivery failure and
   timeout behavior.
5. Test ordinary lifecycle events separately from a controlled provider error.
   A successful prompt does not produce `StopFailure`. Use graceful exit for
   `SessionEnd`; a killed process need not deliver an asynchronous hook.
6. Treat multi-session switching, compaction, and native ID changes as separate
   cases. Do not assume every foreground session emits a startup hook.
7. Independently establish ACP initialization/auth, exact native load, streamed
   content/tools, permissions, cancellation, process restart, daemon reconnect,
   and duplicate-free replay. Capture advertised model/effort/mode IDs instead
   of guessing them. TUI-to-Chat handoff is outside this integration.

Run each claimed platform (macOS, Linux, Windows). Missing account, platform, or
CLI evidence is an explicit gap, never a pass. At this review no installed Junie
binary or authenticated live run was available.

## Official references

- [CLI parameters](https://junie.jetbrains.com/docs/parameters.html)
- [Environment and guideline resolution](https://junie.jetbrains.com/docs/environment-variables.html)
- [Configuration merging and trust](https://junie.jetbrains.com/docs/junie-cli-configuration.html)
- [Guidelines and memory](https://junie.jetbrains.com/docs/guidelines-and-memory.html)
- [Authentication quickstart](https://junie.jetbrains.com/docs/junie-cli.html)
- [Windows installer](https://github.com/JetBrains/junie/blob/main/install.ps1)
- [Stable release feed](https://raw.githubusercontent.com/JetBrains/junie/main/update-info.jsonl)
- [EAP release feed](https://raw.githubusercontent.com/JetBrains/junie/main/update-info-eap.jsonl)
