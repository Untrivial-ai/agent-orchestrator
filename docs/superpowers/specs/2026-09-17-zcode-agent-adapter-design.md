# ZCode Agent Adapter Discovery Design

## Status

Discovery required. No production registration is authorized by this design.

## Objective

Evaluate the official ZCode product as a distinct Agent Orchestrator integration under canonical harness identity `zcode`. Do not reuse `glm-agent`: GLM is a model family and provider offering, while ZCode is the vendor's product name.

The identity decision and initial evidence are recorded in [`docs/research/2026-09-17-glm-agent-product-contract.md`](../../research/2026-09-17-glm-agent-product-contract.md).

## Known official evidence

- [ZCode's vendor-controlled site](https://zcode.z.ai/en) describes an agentic-development environment and distributes the application.
- [ZCode documentation](https://zcode.z.ai/en/docs) covers desktop product behavior.
- [ZCode releases](https://zcode.z.ai/en/changelog) publish desktop builds for macOS, Linux, and Windows; release `3.11.2` has vendor-CDN update metadata with artifact integrity hashes.
- [`zai-org/zcode-plugins`](https://github.com/zai-org/zcode-plugins) is the vendor organization's Apache-2.0 plugin marketplace for ZCode.

This evidence establishes product identity and ownership. It does **not** yet establish a supported terminal process, CLI stability policy, external supervision contract, or native structured Chat protocol. Community packages that bridge or invoke internal headless behavior are not an official contract.

## Required discovery contract

Before implementation, obtain vendor-owned documentation for a supported terminal executable and a pinned vendor-distributed artifact. Record the executable name, installation path, version command, integrity or signature, license, release/support policy, supported platforms, authentication flow, and whether headless use by an external supervisor is supported.

Run non-secret conformance probes against that pinned version. Store sanitized version/help transcripts and structured event fixtures under a ZCode-specific research fixture directory. Do not infer flags or protocols by unpacking the desktop application or copying behavior from a community bridge.

If no public supported terminal contract exists, close discovery as no-go. A desktop-only application cannot satisfy AO's terminal adapter boundary merely because it contains an internal agent runtime.

## Production requirements

A production adapter must pass every requirement below.

### Worker and orchestrator

- One adapter supports selection as either worker or orchestrator.
- The initial user task is delivered deterministically through a documented launch flag or structured protocol, without a timed terminal write.
- AO role instructions use a persistent upstream-supported system or instruction channel. The channel must append to or coexist with ZCode's safety prompt and must not overwrite user-owned configuration.
- Private AO instructions are never concatenated into visible user chat. If ZCode only offers lower-authority guidelines, the integration remains experimental and the UI must describe that weaker semantic accurately.

### Configuration and safety

- Model or mode selection is documented and reapplied on restore.
- AO permission levels have truthful mappings for default, accept-edits, auto, and bypass behavior.
- Restricted sessions can enforce allow/deny tool confinement.
- Observation hooks cannot approve actions. AO-owned hooks or plugins must preserve user hooks and configuration and use an isolated, supported installation mechanism.
- Binary readiness and non-secret authentication readiness are separately reportable.

### Identity and restore

- AO stores the exact provider-native session identifier emitted by a supported structured channel, not scraped terminal text.
- Restore explicitly names that identifier; “latest session” behavior is prohibited.
- Restore reapplies model, permissions, environment, and standing instructions.
- A two-process conformance test proves that the restored process has the same native conversation and history.

### Activity and status

ZCode must expose native hooks or structured events that can be mapped durably to AO facts:

- startup, prompt submission, model start, or tool start → `active`;
- completed turn with an empty composer → `idle`;
- empty prompt waiting for a new instruction → `waiting_input`;
- pending permission or structured input → `blocked`;
- process or native session end → `exited`.

Full signal capability requires startup plus reliable active and settled transitions. AO's generic Kanban derivation consumes these durable facts; no ZCode-specific Kanban logic is added.

### Platform support

Command construction, paths, process lifetime, hooks, and restore must be verified on macOS, Linux, and Windows wherever the official terminal product supports those platforms. Desktop installer availability is not evidence that a terminal contract is portable.

## Chat gate

Chat is a separate optional capability. Register a ZCode Chat driver only if official documentation exposes ACP or another stable structured protocol and conformance tests prove:

- initialization and authentication;
- `session/new` and deterministic exact session loading;
- streamed assistant, reasoning, tool, and plan events;
- permission request and response behavior;
- cancellation;
- provider restart and daemon reconnection;
- history replay without duplicate messages;
- accurate model, mode, attachment, and other negotiated capabilities.

A handshake, an internal desktop IPC endpoint, or a community ACP bridge is insufficient. TUI-to-Chat handoff remains excluded until both interfaces are proven to share the same native session and history.

## AO architecture if accepted

Only after all mandatory gates pass:

- add canonical domain identity `zcode` and a new SQLite migration;
- implement `ports.Agent` and only the optional capabilities proven by fixtures;
- register the adapter centrally in the production agent registry;
- update worker/orchestrator DTO enums, regenerate OpenAPI and TypeScript schemas, and add only genuine frontend branding/configuration surfaces;
- register Chat separately only after its protocol gate passes.

The CLI remains a thin daemon client. The adapter owns only provider-specific command/protocol translation; lifecycle, durable facts, and Kanban derivation stay in their existing generic services.

## Test and release gates

The implementation plan must include table-driven tests for command construction, deterministic prompt delivery, permissions, system instructions, hook/config preservation, activity derivation, native session metadata, exact restore, binary detection, authentication status, model configuration, and platform behavior. Chat additionally requires shared protocol conformance, persistent-host reconnect, replay, approval, cancellation, and capability tests.

After focused package tests, verify the normal repository suites and generated API drift. Authenticated and platform-specific gaps must be reported explicitly; publishing is never a validation step.

## Explicit exclusions

- No `glm-agent` identity, compatibility alias, database value, or UI label.
- No reviewer adapter, reviewer enum, reviewer picker, or reviewer execution support.
- No production adapter or Chat registration based on the currently available desktop documentation.
- No terminal-output scraping when native structured events are required.
- No modification of user-owned global ZCode configuration, hooks, plugins, or credentials.
- No reliance on undocumented internal binaries, desktop IPC, or community wrappers.
