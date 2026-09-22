# Gemini CLI

AO supports Gemini CLI 0.60.0 and newer in Terminal UI mode. Install or update
Gemini from Settings → Agents, then use **Set up Gemini CLI** to authenticate
with the native `/auth` chooser. AO does not read or copy Gemini credentials.

Choose `gemini` as a project's agent or pass `ao spawn --harness gemini`.
The model field forwards a native Gemini model ID through `--model`.

AO starts an interactive session with `--prompt-interactive`, records native
identity from Gemini hooks, and restores that specific conversation with
`--resume`. Workspace trust is scoped to the process with `--skip-trust`.
Hooks in `.gemini/settings.json` preserve unrelated settings and user hooks.
The BeforeAgent hook adds AO's standing instructions as native hook context,
before every submitted turn, including after resume; Gemini retains its own built-in system prompt.

Permission mapping:

| AO mode | Gemini mode |
| --- | --- |
| Default | User's Gemini default |
| Accept edits | `auto_edit` |
| Auto | `auto_edit` (Gemini has no equivalent `auto` mode) |
| Bypass permissions | `yolo` |

Tool allowlists and denylists are rejected because Gemini's native policy engine
has not been integrated. AO reports authentication as unknown until Gemini
itself establishes it; file or API-key presence is not proof of valid access.

Chat mode is not registered. The separate opt-in ACP conformance gate still
requires authenticated streaming, permissions, cancellation, restart, and native
history replay verification before Chat support can be enabled.

Run the installed CLI flag/version check with:

```sh
cd backend
AO_LIVE_GEMINI=1 go test ./internal/adapters/agent/gemini -run TestLiveGeminiReleaseContract
```

This check verifies the executable interface, not authenticated model behavior.
