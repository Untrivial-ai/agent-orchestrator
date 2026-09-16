# Hermes Agent

Hermes Agent is not registered as an AO harness yet. The upstream contract
required for a safe production integration has not passed conformance.

## Gate status

Checked on 2026-09-17 against the `NousResearch/hermes-agent` default branch at
commit `260da4ef6251b29aa830c8c949dbe7c92d58df5f` (the checkout reports version
`0.21.3` in `pyproject.toml`). This is source inspection evidence, not a
successful released-binary conformance run:

- no Hermes executable was installed or supplied through
  `AO_HERMES_CONFORMANCE_BINARY`, so the required isolated end-to-end run could
  not be performed;
- the inspected source accepts a process-local
  `HERMES_EPHEMERAL_SYSTEM_PROMPT`, but it does not expose the required
  per-process system-prompt-file input;
- general plugins are discovered from the active `HERMES_HOME`, project
  `.hermes/plugins`, or Python entry points. No per-process argument that loads
  only one AO-owned observer plugin was found;
- changing `HERMES_HOME` to install an AO observer would also replace the
  user's configuration, credentials, plugins, and native session store, so it
  is not an acceptable isolation mechanism.

Relevant inspected files and SHA-256 hashes:

| File | SHA-256 |
| --- | --- |
| `pyproject.toml` | `a674c321c63c3bfd9fa099fab5957a64092416f7771680b370a4d77e992744ba` |
| `cli.py` | `0a949e43658e5220fb16039b77f1d0cc610ceac204047ca19f46ada3708cac2e` |
| `hermes_cli/personality.py` | `52895363403a0f13124bc7a60dd74e66ad60fb58de02f6e4feeb1ef06c11a8ce` |
| `hermes_cli/plugins_loader.py` | `38280b9a7f83e6f4c0e03dbec91cd986c41b3f3a7eff05772c844e5d4b3c58ff` |

The source does contain ACP and exact-resume machinery, but those capabilities
do not override either mandatory gate above. AO therefore does not claim
Hermes worker, orchestrator, restore, activity, Kanban, or Chat support, and no
Hermes registry, persistence, API, frontend, or reviewer changes are present.

## Required upstream contract

Implementation may resume only after a released Hermes binary provides and an
executable conformance test proves all of the following:

1. A per-process standing-instruction file is applied to fresh and resumed TUI
   and ACP turns, preserves Hermes' built-in safety prompt, and is absent from
   user-message history.
2. A per-process observer argument loads only the supplied AO-owned plugin. The
   observer reports exact native session identity and lifecycle/permission
   events without returning an approval decision or changing user files.
3. Interactive seeded launch and exact-ID TUI restore work on a PTY.
4. ACP initialization, new/load, permissions, cancellation, provider restart,
   replay without duplicates, and detached-host reconnection all pass against
   the same native session ID.
5. Authentication failures are distinguishable from a missing executable
   without exposing credentials.

Until then, the integration plan's standing-prompt stop gate requires AO to
remain unmodified outside this evidence note.
