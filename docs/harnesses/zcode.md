# ZCode terminal integration

**Blocked; not ready for production registration.** The unmodified v3.14.3 TUI does not enable workspace-hook trust at runtime. Its trust CLI accepts exact grants, but the TUI never reads those grants and leaves workspace hooks blocked. Remote conformance reached a real model request with native defaults, project instructions and the exact task, but AO hidden instructions were absent. See [the failing PR run](https://github.com/OrchestratorInc/agent-orchestrator/actions/runs/38009678135). The proposed integration must not ship until the native TUI supplies a supported private instruction/hook channel.

The source cause is `cli/src/tui-prompt-handler.ts`: its app creation omits `workspaceHookTrustEnabled`. `bootstrap/src/app/create-app.ts` forwards that unset option, and `bootstrap/src/app/workspace-hook-trust.ts` requires an explicit `true` before reading trust records. Only the separate protocol host enables it. Granting more hooks or altering provider source in the conformance test would not fix this supported-TUI contract.

AO targets the official Z.ai CLI from [ZCode v3.14.3](https://github.com/zai-org/ZCode/tree/v3.14.3), source commit `29628c9`. Installation is manual because the similarly named npm packages are not official distributions. This integration registers Terminal UI workers and orchestrators; it does not register Chat, reviewers, or interface handoff.

The v3.14.3 GitHub release contains desktop installers only. Its embedded `zcode.cjs` serves the desktop protocol and deliberately excludes `@zcode/tui`; installing that app does not supply AO's interactive CLI. Build the pinned source using Node `24.14.0` and pnpm `10.33.2`: run `pnpm install --frozen-lockfile` and `pnpm --filter '@zcode/cli...' build` from the upstream repository root. Keep the checkout and its installed dependencies together, and expose a `zcode` wrapper on PATH that runs `node /absolute/path/to/ZCode/apps/zcode-cli/packages/cli/dist/zcode.cjs "$@"`. See the upstream README's CLI source development instructions. AO does not install an unrelated npm package or rebuild the provider automatically.

The interactive CLI starts without a task argument: `-p` runs a headless turn and exits. AO waits for the composer before delivering task text through the terminal. Leading dashes remain text. The adapter refuses delivery when required readiness markers are absent, rather than typing into login or trust prompts.

AO merges its hooks into `<workspace>/.zcode/config.json`, preserving user settings and unrelated hooks. ZCode's `hooks.events` nesting is required. Before launch and restore, AO calls `hooks trust status --json` and grants only the exact declaration digests matching its own event, command, source file, and empty matcher. It never grants a complete project hook bundle. Disabled or malformed hook configuration fails closed.

`UserPromptSubmit` returns standing instructions as native `additionalContext`. ZCode adds that context as a system reminder without replacing its default prompt or the visible task. Subsequent turns reread AO's current standing instructions. Native session identity comes from hook `session_id`; restore passes the exact `sess_*` identity to `--resume`. AO opens the native SQLite store read-only and verifies the exact unarchived session, persisted user history, and workspace identity before restore; the native runtime also rejects a missing or archived session. Native global/project storage configuration and session environment overrides are respected. Escape cancels a turn; Ctrl+C is not a cancel key in this release.

AO default explicitly selects `build`, accept-edits selects `edit`, auto selects `build`, and bypass selects `yolo`. An explicit native mode configuration remains supported. Tool deny rules are passed through; unsupported allowlists are rejected. Model selection remains in ZCode's native provider configuration. Settings offers native `zcode login`. A nonempty native credential reports `configured`, never `authorized`. The observer honors `ZCODE_DATA_BASE_DIR` and both encrypted Z.ai and BigModel token entries; it does not decrypt provider credentials.

Native hooks report permission requests as waiting for input and tool activity as active. UserPromptSubmit can be vetoed by later hooks, and Stop can continue the turn: neither callback claims acceptance or completion. A bounded native composer observer reconciles active/idle status and login screens. Coverage remains partial when the composer is absent or edited. AO does not infer process death from missing callbacks.

Source references:

- `apps/zcode-cli/packages/cli/src/run.ts`: TUI, headless prompt, mode and resume routing.
- `apps/zcode-cli/packages/cli/src/hooks-trust-command.ts`: exact declaration trust commands.
- `apps/zcode-cli/packages/bootstrap/src/workspace-hook-trust-cli.ts`: status schema and digest grants.
- `apps/zcode-cli/packages/core/src/hooks/configured-runner-input.ts`: native hook aliases.
- `apps/zcode-cli/packages/core/src/runtime/methods/hooks.ts`: hidden hook context.
- `apps/zcode-cli/packages/core/src/runtime/methods/resume.ts`: missing-session rejection.
- `apps/zcode-cli/packages/tui/src/app-keyboard.ts`: native Escape and Ctrl+C behavior.

Validation for this revision runs on the PR only, as requested. Local test, lint, build, typecheck, and provider-conformance executions were not run. API artifacts were generated. The opt-in source-built TUI fixture compiles unmodified upstream commit `29628c9acdb81b703bbd4080c207a0e7ce5e276e` using its pinned Node/pnpm versions in Linux PR CI, with isolated native configuration and a localhost model provider. The desktop app deliberately omits the interactive TUI dependency, so this fixture does not claim released-binary conformance. It exercises native hook trust, additive private context, default/project instructions, exact SQLite-backed restore after SIGKILL, current-screen activity, Escape cancellation, and continued input. Results remain unverified until that workflow passes; even a pass does not verify authenticated external providers or other platforms.
