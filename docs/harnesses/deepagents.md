# DeepAgents production gate

DeepAgents Code is **not registered as an AO harness**. The upstream contract
has not passed the mandatory safety and lifecycle gates, so it is not offered
for workers, orchestrators, Terminal UI, Chat, or review.

## Candidate upstream

- Product: DeepAgents Code (`dcode` / `deepagents-code`)
- Candidate floor: 0.1.70
- Runtime requirement from the implementation plan: Python >=3.12,<4
- Live conformance input: `AO_DEEPAGENTS_CONFORMANCE_BINARY`, pointing to an
  absolute disposable pinned executable

No executable hash, effective profile paths, provider transcript, or tested OS
matrix is recorded yet because no conformance binary was supplied. Version
0.1.70 is therefore a candidate floor, not a production-supported version.

## Blocking capabilities

The public contract available during this implementation did not establish:

1. A per-process append-system-prompt file (or supported non-destructive
   profile overlay) that preserves the built-in DeepAgents prompt and the
   user's auth, config, sessions, and trust state.
2. A per-process isolated Hooks v2 file that coexists with user, project, and
   plugin hooks without modifying or shadowing them.
3. End-to-end ACP load, replay, permission, cancellation, and reconnect
   behavior tied to the exact native LangGraph thread ID.
4. A local auth-status result that distinguishes configured, missing,
   implicit, and unknown authentication without treating key presence as
   proof of authorization.

Using `DEEPAGENTS_HOME` for an AO-only profile is not an acceptable substitute:
it also replaces user-owned auth, configuration, sessions, and trust state.
Writing `~/.deepagents/AGENTS.md`, `~/.deepagents/hooks.json`, or project-owned
DeepAgents files is prohibited.

## Executing the gate

The package-local validator rejects incomplete evidence and unsafe replacement
profiles. Run its deterministic unit suite with:

```bash
cd backend
go test ./internal/adapters/agent/deepagents -count=1
```

To exercise a pinned upstream candidate:

```bash
cd backend
AO_DEEPAGENTS_CONFORMANCE_BINARY=/absolute/path/to/dcode \
  go test ./internal/adapters/agent/deepagents \
  -run TestDeepAgentsUpstreamConformance -count=1 -v
```

The live gate first verifies the version and advertised per-process prompt,
hook, initial-message, restore, and ACP inputs. It deliberately cannot pass on
static help alone: a future change must add and record disposable-profile PTY,
hook, auth, and ACP transcripts proving fresh/restore behavior, native session
identity, replay, permissions, cancellation, and process reconnect.

Until all evidence exists, AO must not add `deepagents` to domain enums,
registries, migrations, API schemas, product identity assets, or Chat. It must
also never add DeepAgents to reviewer enums or reviewer UI, and must not claim
TUI-to-Chat handoff.
