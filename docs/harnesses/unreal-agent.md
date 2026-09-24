# Unreal Agent Adapter

AO embeds [Unreal Agent](https://github.com/unreallabsai/unreal-agent) v0.1.1 as
a structured Chat harness on macOS and Linux. There is no separate Unreal binary
to install and no Terminal UI mode.

## Configure a provider

The default provider is OpenAI with model `gpt-6-astra`. Make the credential
available to the AO daemon or in the project's environment:

```bash
export OPENAI_API_KEY=...
```

The embedded harness also accepts Unreal's provider environment variables:

| Variable | Purpose |
| --- | --- |
| `UNREAL_HARNESS_LLM_PROVIDER` | `openai`, `openai-codex`, `openrouter`, `fireworks`, or `ollama` |
| `UNREAL_HARNESS_LLM_MODEL` | Provider model when AO has no model override |
| `UNREAL_HARNESS_LLM_API_KEY` | Provider-neutral API-key override |
| `UNREAL_HARNESS_LLM_BASE_URL` | Provider endpoint override |
| `UNREAL_HARNESS_LLM_MAX_ATTEMPTS` | Positive request-attempt limit |

Provider-specific keys are `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, and
`FIREWORKS_API_KEY`. `openai-codex` uses the Codex subscription environment or
auth file understood by Unreal; `ollama` needs a model but no API key.

## Run

Choosing `unreal-agent` without explicit mode or approval overrides defaults the
session to Chat with `bypass-permissions`, including for `ao spawn` and delegated
tasks. AO refuses explicitly selected safer permission modes because Unreal
v0.1.1 does not expose an interactive approval channel or a provider-enforced
read-only sandbox.

The harness supplies Bash, image inspection, and workspace skills from
`.harness/skills`. AO-supplied MCP servers, prompt attachments, additional
workspace roots, and mid-conversation model changes are not supported yet.

## Persistence

AO keeps the provider process alive in its authenticated detached Chat host.
Unreal's local session log is the native conversation record; AO projects its
events into SQLite and acknowledges each event only after that projection is
durable. Unacknowledged events replay after daemon replacement or provider
restart, while a dead provider resumes from Unreal's stored session.

All Unreal-owned state remains under `AO_DATA_DIR` (normally `~/.ao`). Windows
builds keep working, but this harness reports unavailable there because Unreal
v0.1.1's process primitives are Unix-only.
