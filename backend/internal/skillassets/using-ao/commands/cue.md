# ao cue

Create and inspect reusable Cues for a local AO project. Both orchestrators and workers can use this command from their sessions. The project defaults to `AO_PROJECT_ID`, then the current session's project, then the registered project containing the current directory. Pass `--project <id>` when the target differs.

When the user asks to turn a repetitive workflow into a Cue, first list the project's Cues to avoid creating a duplicate. Choose an **agent Cue** for a reusable natural-language instruction. Choose a **command Cue** only when the exact shell command is known. If the requested behavior is ambiguous, clarify the missing instruction or command before creating it. After creation, tell the user the Cue's name, kind, and what it will run.

```text
ao cue list [--project <id>] [--json]
ao cue create --name <name> (--prompt <instruction> | --command <shell-command>) [--description <text>] [--project <id>] [--json]
```

`--prompt` creates an agent Cue. `--command` creates a command Cue. Supply exactly one. Use `--json` to inspect full definitions or the created Cue ID. The daemon validates length, project existence, and unique names; its API error code and request ID are preserved on failure.

Examples:

```bash
ao cue create --name "Review current PR" --prompt "Review the current PR for correctness and report actionable findings."
ao cue create --name "Run backend tests" --command "cd backend && go test ./..."
ao cue list --json
```

This CLI intentionally has no update or delete subcommands. If a matching Cue already exists, report it and ask the user to edit or delete it in project settings instead of replacing it.
