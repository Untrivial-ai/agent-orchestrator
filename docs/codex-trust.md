# Codex configuration trust

Task permissions do not approve executable repository configuration. AO leaves
project trust to the user's native Codex decision. Chat binds the provider process
to the workspace and omits `cwd` from `thread/start`, because Codex interprets an
explicit writable startup directory as project consent. Native restore still
receives the requested workspace so a conversation can move between worktrees.
Start, restore and MCP reload must preserve unknown, denied and approved trust
states without changing the user's configuration.

AO passes activity hooks through the native session-flags layer. It approves each
AO-authored command by source, event and exact normalized content hash. That
approval does not cover project, user or plugin hooks. A changed hook needs a new
review. AO sets only `trusted_hash`, preserving an explicit `enabled=false`.
Unknown hook events are rejected until their native identity has been verified.
There is no fallback to `--dangerously-bypass-hook-trust`.

The source-key and canonical-hash formats are provider contracts. The native
tests run against Codex 0.153.4 with isolated homes, ordinary and linked checkouts,
marker commands and a local fake Responses server. They check usable model turns,
native restore, MCP reload and callback execution as well as blocked commands.
CI also exercises the desktop minimum, 0.146.0, and the existing Cloud image pin,
0.147.0. Run the same tests against a proposed provider upgrade before updating
a pin:

```sh
cd backend
AO_TEST_CODEX_BINARY=/absolute/path/to/codex go test -race -run TestNativeCodex ./internal/adapters/chatdriver/codexappserver
```

Cloud uses `../backend` even with `GOWORK=off`. Build `cloud/Dockerfile` with the
repository root as context. The trust workflow checks the standalone Cloud module
and builds the production commands, so a workspace override cannot hide a stale
backend dependency.

## Existing sessions

New launches and native restores receive the new hook policy. An already running
TUI process retains its old flags until it is stopped and restored. Replace old
Cloud worker images and restore the native conversation and workspace through the
normal session workflow. Reconnecting to a surviving Chat provider does not rerun
startup or revoke trust it previously saved.

AO does not erase the native `projects` map or guess which saved approvals came
from a person. Earlier writable Chat starts may have persisted project trust. A
user who did not intend that approval must review it in the active account's
Codex configuration. This change prevents new implicit Chat startup approvals;
it cannot recover the provenance of old entries.
