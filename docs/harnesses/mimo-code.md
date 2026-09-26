# MiMo Code Adapter

AO supports [MiMo Code](https://github.com/XiaomiMiMo/MiMo-Code) v0.1.14 or
newer as a terminal worker or orchestrator. The adapter uses the user-installed
`mimo` executable; AO does not bundle the provider.

## Install and authenticate

Install the official npm package:

```bash
npm install -g @mimo-ai/cli
mimo providers login
```

AO checks local credential presence with `mimo providers list`. A discovered
credential is reported as **configured**, not authorized, because this local
command does not make a provider request. MiMo's anonymous free model may work
even when readiness is unknown.

## AO integration

- New tasks are delivered with `mimo --prompt`; model overrides use `--model`.
- AO passes `--trust` for its supervised session worktree.
- AO installs an observation-only hook at `.mimocode/hooks/ao-activity.ts` and
  the `using-ao` skill under `.mimocode/skills/`.
- Standing instructions use an AO-owned, session-specific custom agent under
  `.mimocode/agents/`; user agents and hooks are preserved.
- Native IDs beginning with `ses_` are captured from lifecycle events and
  restored exactly with `mimo --session <id>`.

Permission modes map to MiMo's native controls: default keeps configured
behavior, accept-edits allows edits only, and auto/bypass use MiMo's documented
`--dangerously-skip-permissions` mode. That native mode preserves explicit
denies and also opts into irreversible-delete approval; MiMo currently exposes
no separate startup switch for AO's narrower auto mode.

This integration is TUI-only. It does not register MiMo Code for Chat,
reviewers, interface handoff, or first-class nested-agent sessions.
