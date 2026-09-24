# Codewhale Adapter

AO supports Codewhale v0.10.0 and newer as an interactive Terminal UI worker
and orchestrator. Install the user-owned `codewhale` (or `codew`) executable
from [Hmbown/Codewhale](https://github.com/Hmbown/Codewhale) and keep it on
`PATH`; AO does not run a remote-shell installer.

Fresh sessions use an explicit workspace, skip the onboarding screen, disable
implicit resume, and deliver the initial task through `--prompt`. AO maps its
default and accept-edits modes to Codewhale's `on-request` approval policy,
auto to `auto`, and bypass-permissions to Codewhale's explicit `--yolo` mode.
Model overrides are passed through `--model`, and the picker is populated from
`codewhale models --json`.

AO places its private standing instructions in the ignored, AO-owned workspace
rule `.codewhale/rules/ao-agent-orchestrator.md`. It refuses to overwrite a
foreign file at that path. The instructions are never sent as a user message.

For lifecycle tracking, AO creates a per-session managed overlay under
`AO_DATA_DIR/agent-runtime/codewhale/sessions/<session>/`. The overlay contains
only Codewhale's `[lifecycle_outbox]` configuration, so the user's ordinary
configuration and hooks remain active. It writes to a per-session JSONL outbox
and posts the same events to the loopback daemon with AO's current runtime
generation in the URL. AO ignores Codewhale's internal subagent events and does
not claim permission-blocked detection.

AO refuses the launch when Codewhale already has a managed configuration source
because replacing it would violate administrator or user policy. AO does not
set `CODEWHALE_HOME`; authentication, configuration, trust, and native sessions
remain in the user's normal Codewhale profile.

The lifecycle stream persists Codewhale's `sess_*` native ID. Restore uses only
that exact identity:

```bash
codewhale --workspace <workspace> --skip-onboarding --resume <sess_id>
```

Codewhale Chat, reviewer support, TUI/Chat handoff, and first-class nested-agent
sessions are not part of this integration.
