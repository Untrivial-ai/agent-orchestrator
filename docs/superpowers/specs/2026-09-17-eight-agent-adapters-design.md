# Eight Agent Adapter Expansion Design

## Objective

Add production-quality Agent Orchestrator integrations for Gemini CLI, JetBrains Junie, Hermes Agent, DeepAgents, Qoder CLI, CodeBuddy Code, Cortex Code, and the product currently described as GLM Agent.

Each integration is planned and delivered independently. An agent is registered only when its implemented capabilities are truthful and its minimum supported upstream version has been verified. Reviewer support is excluded from all eight integrations.

## Product requirements

Every registered agent must support:

- selection as a worker or orchestrator;
- delivery of the initial task without terminal-input races;
- persistent AO role instructions without overwriting user configuration or weakening the provider's built-in safety prompt;
- binary and authentication readiness reporting;
- a model or mode configuration surface;
- durable session activity suitable for AO status and Kanban derivation;
- native session identity and deterministic session restore;
- safe preservation of user-owned configuration and hooks;
- Linux, macOS, and Windows behavior where the upstream CLI supports those platforms.

Chat is a separate capability. It is registered only when a stable structured protocol supports prompt streaming, permissions, cancellation, durable conversation identity, and session loading. TUI-to-Chat handoff is excluded until an integration test proves that both interfaces share the same native conversation and history.

## Existing AO architecture

TUI integrations implement `ports.Agent` and optional capabilities in `backend/internal/ports/agent.go`. Agent identity is declared in `backend/internal/domain/harness.go`, and production construction is centralized in `backend/internal/adapters/agent/registry/registry.go`.

Activity callbacks enter through `ao hooks`, are normalized by `backend/internal/adapters/agent/activitydispatch`, and persist durable facts through lifecycle. Kanban placement remains generic: `backend/internal/service/session/kanban.go` derives presentation from session activity and PR facts. No agent-specific Kanban implementation is required.

Chat integrations implement `ports.ChatDriver` and are explicitly registered in `backend/internal/adapters/chatdriver/registry/registry.go`. AO already has a complete Chat UI and persistent provider-host architecture. Codex uses its native app-server protocol; Claude Code, OpenCode, Cursor, Droid, Kimi, Kimchi, Pi, and OMP use ACP-backed drivers.

## Shared integration policy

### Identity and persistence

Use these canonical harness identifiers:

- `gemini`
- `junie`
- `hermes`
- `deepagents`
- `qoder`
- `codebuddy`
- `cortex-code`
- `glm-agent`

A new SQLite migration widens the `sessions.harness` constraint for the identities that are actually ready to register. Existing migrations are never modified. `glm-agent` is not added to the database or registry until an official product contract is identified.

### Worker and orchestrator behavior

The same adapter serves both roles. AO remains responsible for constructing the role-specific standing instructions in `session_manager/prompt.go`. The adapter only delivers those instructions through an upstream-supported system or instruction channel and maps the user task through a deterministic prompt mechanism.

An adapter must not concatenate private AO instructions into visible user chat. A documented guidelines or context channel may be used only when the UI and documentation describe the weaker semantics accurately.

### Activity and Kanban

Native events map to AO facts as follows:

- prompt submitted, model started, or tool started: `active`;
- turn completed with an empty composer: `idle`;
- empty prompt awaiting a new instruction: `waiting_input`;
- pending permission or structured input: `blocked`;
- provider process or native session ended: `exited`.

An integration is marked fully signal-capable only when it produces a startup signal plus reliable active and settled transitions. Permission hooks must never approve an action as a side effect of observation. Hook files written to a worktree must be AO-owned, preserve user hooks, and be covered by an AO-managed sibling `.gitignore`.

### Restore

AO stores the exact provider-native session identifier. Restore always names that identifier explicitly; "latest session" commands are prohibited. Restore commands reapply model, permission, environment, and standing-instruction configuration because those settings may not be part of provider history.

### Chat

Native ACP drivers reuse AO's shared ACP transport and detached persistent host. A provider is added to the Chat registry only after conformance tests establish:

- initialization and authentication behavior;
- `session/new` and deterministic `session/load`;
- streamed assistant, reasoning, tool, and plan events;
- permission request and response behavior;
- cancellation;
- provider restart and daemon reconnection;
- history replay without duplicate messages;
- accurate advertised configuration and attachment capabilities.

Basic ACP transport alone is insufficient for registration.

### API and UI

Production harnesses are added to agent/session DTO enums and regenerated OpenAPI and TypeScript schemas. Reviewer enums and reviewer registries are deliberately unchanged. Agent selection uses backend readiness inventory; frontend work is limited to real brand assets, avatar mapping, and tests unless an agent exposes a genuinely new configuration shape.

## Per-agent capability decisions

### Qoder CLI

Qoder is the strongest complete integration candidate. Its documented CLI provides caller-assigned session IDs, interactive initial prompts, native system-prompt append, model and reasoning controls, all AO permission levels, tool allow/deny lists, restore by exact ID, lifecycle hooks, and native ACP.

Deliver TUI worker/orchestrator support and ACP Chat in the first production version. TUI-to-Chat handoff remains excluded until shared identity is proven end to end.

### Gemini CLI

Gemini provides interactive initial prompts, caller-assigned session IDs, exact restore, model and approval flags, native lifecycle hooks, and ACP. Its `GEMINI_SYSTEM_MD` mechanism replaces the provider's entire built-in system prompt.

AO must generate a version-matched combined system prompt in AO-owned storage: obtain the upstream default through `GEMINI_WRITE_SYSTEM_MD`, append AO instructions, cache it by executable fingerprint, and launch with `GEMINI_SYSTEM_MD`. If this process cannot be made deterministic for the pinned version, the integration must remain experimental and describe SessionStart `additionalContext` as guidelines rather than a system prompt.

### CodeBuddy Code

CodeBuddy has a public CLI package and a rich native ACP implementation with session configuration, history loading, permissions, and provider extensions. Its terminal hooks are beta, while public evidence for exact TUI launch, system-prompt, permission, and restore flags is incomplete.

The implementation begins with an executable conformance fixture against a pinned release. ACP Chat and TUI are registered only after the fixture proves a persistent AO instruction channel and the exact session lifecycle. No Claude-compatible flags are inferred merely because the product has similar concepts.

### Hermes Agent

Hermes exposes a capable ACP protocol and native session persistence. Its public interactive CLI does not expose a verified append-system-prompt option. Rich observer hooks exist through the plugin API, but a safe isolated plugin installation path must be demonstrated.

The driver and conformance tests may be implemented first, but production registration is gated on a per-process AO instruction channel that does not modify user `SOUL.md` or global configuration. Terminal support additionally requires an AO observer plugin that captures the native ID and emits activity events.

### DeepAgents

DeepAgents Code (`dcode`) exposes persistent interactive sessions, exact resume, hooks, and ACP. Its public CLI does not expose a direct system-prompt flag, and selecting a different `DEEPAGENTS_HOME` would also replace user authentication, configuration, and session state.

Production registration requires an upstream append-system-prompt file option or a supported profile overlay. Project or user hook files must not be mutated without an isolated AO-owned hook configuration mechanism. ACP and TUI use the same gating requirements.

### JetBrains Junie

Junie can launch persistent TUI sessions and inject an AO-owned guidelines file. Guidelines are lower-authority context rather than a privileged system prompt. Session ID capture relies on hooks that are currently EAP, and a synchronous permission hook may approve an action simply by returning success.

The first implementation is an experimental TUI adapter only after stable version-gated hooks can observe activity without changing permission decisions. Restore and Chat remain disabled until native ID capture and ACP load, permission, cancellation, and replay behavior pass conformance tests.

### Cortex Code

Cortex Code exposes structured stream-JSON execution, stdio permission mediation, session ID events, and version-dependent resume. It does not expose a verified native ACP endpoint.

Implement it as a dedicated structured controller rather than displaying JSON in a terminal. Production registration requires verified model and appended-system-prompt flags plus a pinned version whose explicit-ID resume works. A custom Chat driver may be added after the stream protocol is normalized into AO conversation events.

### GLM Agent

No official Zhipu product contract for a coding-agent CLI named GLM Agent was found. GLM Coding Plan is a model/provider offering used through existing agent CLIs, and community ACP bridges are not an official agent identity.

The separate GLM plan is therefore a discovery and acceptance-gate plan. AO must not add a harness, database identity, or UI option until the vendor provides an official executable/package, CLI reference, system-instruction channel, activity protocol, durable session restore, and authentication contract. If the intended product is ZCode, it receives a new design under that actual identity.

## Delivery structure

Create one implementation plan and branch per agent. Each plan begins with its own upstream contract/conformance task, owns only that agent's adapter and optional Chat driver, and includes the minimum shared registry/schema edits necessary to make that agent independently shippable. This intentionally repeats small registry edits across plans so no agent depends on an unshipped umbrella branch.

The recommended delivery order is:

1. Qoder
2. Gemini
3. CodeBuddy
4. Hermes
5. DeepAgents
6. Junie
7. Cortex Code
8. GLM Agent discovery gate

## Testing and release gates

Each production plan must include table-driven unit tests for command construction, prompt delivery, permissions, system instructions, hook preservation, activity derivation, session metadata, restore, binary detection, authentication status, and model configuration. Chat plans additionally include shared ACP conformance, persistent-host reconnect, history replay, approval, cancellation, and capability tests.

Each agent is verified first with focused package tests, then:

```bash
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm run frontend:typecheck
cd frontend && npm run build
npx @redwoodjs/agent-ci run --all
```

Authenticated or platform-specific tests that cannot run locally must be reported as explicit CI/manual gaps. Publishing is never used as validation.

## Explicit exclusions

- No reviewer adapter, reviewer enum, reviewer picker, or reviewer execution support for these eight agents.
- No TUI-to-Chat handoff without proven shared native identity and history.
- No terminal-output scraping when native hooks or structured events are available.
- No writes to user-owned global agent configuration when an AO-owned overlay cannot be used.
- No fake capability claims for restore, blocked state, system-prompt authority, or Chat.
