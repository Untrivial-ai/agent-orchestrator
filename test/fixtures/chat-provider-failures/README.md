# Native provider-failure verification

Opt-in fault injection for PR #5227. `provider.mjs` speaks enough ACP and Codex
app-server JSON-RPC to exercise the real daemon, SQLite projection, native
Electron preload, and renderer. It never contacts a provider or reads credentials.
This does **not** certify a live provider's availability, billing, or login flow.

## Isolated setup

Use Node 24 and the repository's normal Electron dev dependencies. Follow
`.agents/skills/ao-desktop-dev/SKILL.md`; do not use your normal AO profile.

1. Choose a dedicated directory under `~/.ao/dev/pr-5227`. Set `AO_DATA_DIR` to
   its `data` directory, `AO_RUN_FILE` to its `running.json`,
   `AO_DEV_ELECTRON_DIR` to its `electron` directory, and `CODEX_HOME` to a new
   `codex-home` directory. Set `AO_PORT=3307`.
2. Put executable wrappers named `codex` and `claude` on a test-only PATH. Each
   should execute Node with the absolute path to `provider.mjs`, forwarding all
   arguments. Point `AO_CLAUDE_ACP_COMMAND` at another wrapper that adds `--acp`.
   Ensure the login-shell environment probe retains this PATH. The evidence run
   used a test-only shell wrapper (normal commands delegated to `/bin/zsh`).
3. Codex account bootstrap needs a file in the isolated `CODEX_HOME`, even though
   the fixture does not read it. Create `auth.json` with mode 0600 containing
   `{"OPENAI_API_KEY":"ao-fault-injection-not-a-real-credential"}`. Never copy real
   account credentials into this fixture setup.
4. Create a disposable Git repository with an initial commit on `main`. Add its
   own absolute path as `origin`, fetch it, and set `origin/HEAD` to `origin/main`.
   Register that repository through AO as project ID `pr5227-evidence`.
5. From `frontend/`, run `npm run dev -- -- --remote-debugging-port=9337` with
   those environment variables. Confirm the native app is using daemon port
   3307, renderer URL `http://localhost:5173`, and the isolated data directory.

From the repository root:

```sh
node test/fixtures/chat-provider-failures/verify-electron.mjs
```

The verifier attaches to that native Electron instance, creates two disposable
sessions, and sends messages through the real composer. It checks quota failure,
one terminal explanation, safe links, reload, retry/recovery, cancellation,
two retry episodes in one turn, and the existing reauthentication state/banner.
The fixture delays terminal responses for 12 seconds so retry progress is
observable; the verifier uses condition-based assertions, not UI sleeps.

Screenshots and sanitized conversation snapshots overwrite files in
`docs/pr-evidence/pr-5227/`. Stop only this test instance and kill its disposable
sessions through AO when finished. If switching back to Node-based tests after
Forge rebuilds `better-sqlite3` for Electron, rebuild it for Node first.
