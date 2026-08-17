# Quick Reference

Natural-language-to-command mappings for common AO tasks.

| You want to... | Command |
|---|---|
| Show me this webpage / open this page | `ao preview "<url>"` |
| Start an existing configured dev app | `ao preview start [configuration]` |
| Check or stop the worker's managed dev app | `ao preview status` / `ao preview stop` |
| Show this Markdown or HTML file without a server | `ao preview "<workspace-path>"` |
| Hand off a newly created browser-displayable artifact | `ao preview "<workspace-path>"` immediately after writing the primary artifact |
| Inspect and verify this webpage as the agent | `ao browser open "<url>"`, then `ao browser snapshot` |
| Click or fill a page element | `ao browser snapshot`, then `ao browser click <ref>` or `ao browser fill <ref> "<text>"` |
| Check frontend runtime failures | `ao browser errors` and `ao browser console` |
| Diagnose a request/API/CORS/auth/redirect failure when normal page evidence is insufficient | `ao browser network start`, reproduce once, then `ao browser network stop` |
| Check network capture without enabling it | `ao browser network status` or `ao browser network list` |
| Open the user's real Chromium debugging surface | `ao browser devtools open` |
| Close the shared DevTools window when explicitly requested | `ao browser devtools close` |
| Capture the page | `ao browser screenshot [path]` |
| Spawn a worker on issue N | `ao spawn --project <p> --issue N --name "<=20 chars>" --prompt "..."` |
| Message a running agent | `ao send --session <id> --message "..."` |
| Kill a session | `ao session kill <id>` |
| List sessions | `ao session ls` |
| Register a repo as a project | `ao project add --path <abs-path> --name <name>` |
| List projects | `ao project ls` |
| Rename a session | `ao session rename <id> "<name>"` |
| Restore a killed session | `ao session restore <id>` |
| Clean up terminated sessions | `ao session cleanup` |
| Make a Docker container this session starts survive AO cleanup | `docker run --label ao.session=$AO_SESSION_ID --label ao.spare=true ...` |
| See a session's details | `ao session get <id>` |
| Open the desktop app | `ao start` |
| Check the daemon is up | `ao status` |
| Run health checks | `ao doctor` |
| Clear the preview panel | `ao preview clear` |
| List orchestrator sessions | `ao orchestrator ls` |
| Claim an existing PR for the current session | `ao session claim-pr <pr-ref>` (`AO_SESSION_ID`) |
| Claim an existing PR for another session | `ao session claim-pr <id> <pr-ref>` |
| Submit a code review verdict | `ao review submit <session-id> --run <run-id> --verdict approved` |
| Configure a project's default branch or model | `ao project set-config <id> --default-branch <branch> --model <model>` |
| Import projects from a legacy AO install | `ao import --dry-run` (preview), then `ao import -y` |
