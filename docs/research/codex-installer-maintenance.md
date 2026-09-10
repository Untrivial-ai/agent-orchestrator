# User-installed Codex maintenance (#5003)

AO keeps the Codex selected by its normal resolver. Settings → Harnesses shows
its effective version, detected installer, available version, and diagnostic
paths. Checking versions does not update Codex or affect authentication and
protocol compatibility. Unsupported/custom ownership remains manual-only.

The daemon accepts only the displayed ownership token for **Update now**. It
rechecks the executable, wrapper target, installer and installation context
under the existing installer job boundary. Conflicting jobs queue. AO currently
serializes all installer jobs conservatively, including different harnesses
sharing npm/Homebrew state, rather than adding a general maintenance scheduler.

The action requires all AO Codex sessions to be explicitly stopped. A launch
interlock prevents new sessions racing the installer. Neither the update nor
verification terminates tasks. Restarting AO alone is insufficient because
provider hosts may survive it. External processes can keep their old runtime;
external installers may remove old helper files. This shared-installation flow
provides neither immutable pinning nor guaranteed rollback or model entitlement.

After every attempted command, including nonzero exits/timeouts, the daemon
refreshes installation/version-source information and readiness, and refreshes
all cached project catalogs using fresh provider processes. It preserves the
selected binary, project directory, account overlay and configuration. A zero
exit is only command completion: unchanged/outdated/missing/unverifiable versions
or failed readiness/model refresh produce a failed job. Useful cached catalogs
and readiness retain their existing stale/error behavior. The application shell
invalidates frontend queries on every terminal update outcome, even after
Settings closes. Models continue to come from paginated provider `model/list`.

## Installer evidence and current primary sources

Sources inspected 2026-09-10. T3 reference is pinned to
[`8b2838e`](https://github.com/pingdotgg/t3code/tree/8b2838e0e8a73d3fa6476940445c372e47b99db4).
See its [maintenance resolver](https://github.com/pingdotgg/t3code/blob/8b2838e0e8a73d3fa6476940445c372e47b99db4/apps/server/src/provider/providerMaintenance.ts),
[Codex driver](https://github.com/pingdotgg/t3code/blob/8b2838e0e8a73d3fa6476940445c372e47b99db4/apps/server/src/provider/Drivers/CodexDriver.ts),
and [runner](https://github.com/pingdotgg/t3code/blob/8b2838e0e8a73d3fa6476940445c372e47b99db4/apps/server/src/provider/providerMaintenanceRunner.ts).

| Installation evidence | Daemon-derived update | Available version |
| --- | --- | --- |
| Standalone release payload behind the owning `packages/standalone/current` link; selected CLI advertises `update --help` | Selected `codex update`, with installation `CODEX_HOME`, visible `CODEX_INSTALL_DIR`, noninteractive latest release | npm latest, as in T3 |
| Homebrew Cellar/Caskroom path, owning `brew --prefix`, and package file list | Owning `brew upgrade --formula/--cask codex` | Owning `brew info --json=v2` stable formula/cask version |
| Global npm package manifest and exact global root/prefix, including Node-version installations and Windows shims/native payloads | `npm install -g --prefix <owner> --allow-scripts=@openai/codex @openai/codex@latest` | npm latest |
| pnpm global package matching owning `pnpm root -g` | Owning `pnpm add -g @openai/codex@latest`, preserving global context | npm latest |
| bun bin/global package layout matching `bun pm -g bin` | Owning `bun i -g @openai/codex@latest`, preserving installation context | npm latest |
| Vite+ dispatcher/trampoline, matching bin/package metadata and installation directories | Owning `vp i -g @openai/codex`, preserving Vite+ directory overrides | npm latest |

Codex's [Unix installer](https://github.com/openai/codex/blob/main/scripts/install.sh),
[Windows installer](https://github.com/openai/codex/blob/main/scripts/install.ps1),
[install context](https://github.com/openai/codex/tree/main/codex-rs/install-context),
and [native update action](https://github.com/openai/codex/blob/main/codex-rs/tui/src/update_action.rs)
identify the shared standalone home and visible installation directory. An
authentication-only `CODEX_HOME` overlay is not its update destination. Old CLIs
without native update support and explicitly pinned release paths stay manual.

T3 documents the npm 12 script allowlist and excludes mise-managed non-Node tools
from global npm ownership. The prefix is mandatory; finding a package manager on
PATH alone is not authorization. Homebrew never falls back to npm. Custom bun
layouts that cannot be proven from its reported bin and package tree stay manual.
See also [pnpm global add](https://pnpm.io/cli/add) and
[bun package commands](https://bun.sh/docs/pm/cli/add).

Current [Vite+ source](https://github.com/voidzero-dev/vite-plus/blob/main/crates/vp_shared/src/dirs.rs)
and [CLI source](https://github.com/voidzero-dev/vite-plus/tree/main/crates/vp_global_cli/src/commands/env)
define `bins/codex.json`, `packages/@openai/codex.json`, UUID installation IDs,
Windows `.shim` sidecars, and the read-only `VP_DUMP_DIRS` diagnostic. Legacy
single-home dispatchers require matching ownership metadata. Unrecognized
versions/layouts remain manual rather than trying another installer.

The [official app-server contract](https://developers.openai.com/codex/app-server/)
defines `model/list`, hidden entries and pagination. Runtime updates do not imply
account access to a particular returned model; AO introduces no model IDs here.

## Bounds and validation

Advisories coalesce concurrent reads and cache for one hour, or five minutes after
failure. The combined read has a 16-second context; local probes have 3-second
limits, HTTP has a 4-second limit, and metadata/output reads cap at 256 KiB.
Explicit refresh bypasses the cache. Jobs retain the existing 15-minute installer
timeout and bounded output, followed by an independent 90-second refresh budget.
A final ownership check catches retargeting during model discovery.

Migration **0131_queued_agent_installer_jobs.sql** extends the durable installer
status constraint; recovery marks queued jobs interrupted. Version 0130 belongs
to #5040's independent shell-terminal migration. This issue's former unmerged
0130 was used exclusively in disposable tests; no persistent database repair is
needed or performed.

Tests use injected filesystem/command/HTTP seams, temporary payloads, in-memory
provider pipes and disposable SQLite databases. They never update a real Codex
installation. Windows runner coverage exercises the real batch command boundary.
