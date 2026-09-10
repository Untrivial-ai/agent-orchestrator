---
name: bug-triage
description: Help clarify human bug reports, search for duplicates, and gather diagnostic evidence separately from a concise issue draft.
---

# Bug triage

Help the reporter describe what they observed and add useful evidence. A short human
observation is enough to report a bug; reproduction, logs, and a diagnosis are optional.
Use this skill for reports from chat, an existing issue, or a local user.

## Clarify the observation

Read the available report, screenshots, and follow-ups first. Preserve the original
reporter's words and attribution, including when someone else relays their report.
Don't infer a GitHub username from a chat display name.

Ask only questions that would materially clarify the symptom, a few at a time. Don't
repeat questions already answered or turn these suggestions into required fields:

- What did you do, and what happened instead of what you expected?
- Where did it happen, and does it happen every time or only sometimes?
- For a visual problem, can you share a screenshot or short recording showing it?
- If relevant, which AO version and OS were you using, and did it start after a change?

If a screenshot is unavailable, accept a description. If the reporter cannot remember
steps or reproduce the problem, preserve that uncertainty and proceed with what is
known. Never invent steps or make the reporter diagnose the bug before accepting it.

## Keep the report and investigation separate

Draft a specific symptom-based title and a short body, normally one paragraph or a few
bullets. Include only details supplied directly by the human: their observation,
expected behavior, steps, frequency, environment, and impact when they provided them.
Light editing for clarity is fine; preserve meaning and uncertainty. Omit empty
sections and unnecessary background. Do not add agent-inferred impact, root cause,
confidence scores, or proposed fixes to the report.

Example human report:

> After I switched projects, the terminal stayed blank. I expected to see the session
> prompt. It happened twice today on macOS; I don't know how to reproduce it reliably.

Keep agent-discovered information in a separate attachment labeled **Agent-collected
evidence**, even when it confirms the report. Include only relevant excerpts and:

- The command or read-only query used, collection time, and relevant AO version or
  checkout commit, so another person can interpret the result.
- Observed output, screenshots, or reproduction results, with enough context to verify
  the finding. Distinguish the reporter's environment from a separate test build.
- A brief explanation of what the evidence supports and its limits. Label hypotheses
  explicitly; don't present a code-path guess as a confirmed cause.

If no useful evidence is available, deliver the human report alone. Never pad it with
speculation or claim a reproduction succeeded when it didn't.

## Gather relevant evidence

Use diagnostics that fit the symptom, within the user's requested scope. Prefer
read-only checks against the affected installation. Don't restart sessions, upgrade
AO, change live data, or alter the user's checkout just to collect evidence.

AO uses a Go daemon and an Electron/React frontend. Read `docs/architecture.md` for
current boundaries and locate actual files with `rg` before citing code. Do not rely
on the old TypeScript implementation or assume which runtime backs a session.

Before trusting diagnostics, identify the AO executable and affected daemon. A PATH
lookup may resolve to a legacy install. Check the executable's version and status,
and the configured run file/data directory (`AO_RUN_FILE` / `AO_DATA_DIR` when set).
The primary daemon normally uses loopback port 3001; port alone does not establish
which version or installation is affected. Consult the installed CLI help for commands.

Collect only what helps explain this symptom:

| Symptom | Potential evidence |
| --- | --- |
| UI rendering or interaction | Screenshot/recording, relevant console errors, affected view |
| Session or terminal state | Affected session's daemon state, relevant log excerpt, runtime details |
| Daemon or CLI failure | Version/status, exact command and error, relevant daemon logs |
| Persisted state mismatch | Relevant rows and schema from a read-only SQLite query |

Use the daemon API/CLI for state first. If database evidence is needed, discover the
actual database location and schema, open it read-only, and select only relevant rows
and columns. Never attach an entire database or broad log dump. Redact tokens,
credentials, private prompts, and unrelated personal or repository data from every
artifact, including screenshots. Keep diagnostic artifacts under `~/.ao` and out of
Git commits. Share a useful excerpt rather than a wall of output.

Try a safe reproduction when practical, recording the build actually tested. Failure
to reproduce does not invalidate the human report. Code tracing or dependency research
is optional when it can add concrete evidence. Keep findings in the attachment.

## Check for duplicates

Search open and closed issues and related PRs using the symptom or exact error:

```bash
gh issue list --repo Untrivial-ai/agent-orchestrator --state all --search '<symptom or error>'
gh pr list --repo Untrivial-ai/agent-orchestrator --state all --search '<symptom or error>'
```

Read likely matches before calling a report a duplicate. If one matches, give the
reporter its link and draft only their new observation for a comment, with evidence
attached separately. If a closed issue appears to have regressed, explain that to the
reporter rather than silently treating it as resolved. If search is unavailable,
state that briefly; it does not block preparing the report.

## Preserve attribution and submission scope

Recommend that the reporter submit the issue or comment from their own GitHub account.
Do not use AO Bot or another shared bot account to file human reports on their behalf.
A local coding agent may help draft and gather attachments. If explicitly asked to
submit using the reporter's account, verify the authenticated identity first; do not
silently publish under a different identity. Existing authorization still applies;
triage alone is not permission to publish.

For an existing issue, preserve its human body and attribution. Add evidence as a
separate attachment in a clearly labeled comment when authorized. Don't rewrite the
report into an investigation narrative.

Use the GitHub attachment UI when available. Otherwise give the reporter the local
artifact paths and ask them to drag the files into the issue or comment. Never invent
attachment URLs or create asset branches to host diagnostics. Keep the report body
limited to human details and attachment links, with analysis inside the attachments.
When submitting via CLI, use `--body-file` for reviewed text. Apply only existing,
relevant labels; don't add priority/confidence prose just to fill a template.

Finish with the concise draft (or issue link if submitted), any duplicate link, and
attachment paths/links. Do not automatically file an issue, implement a fix, push a
PR, or spawn a worker. Follow the user's requested scope and the repository's normal
contribution workflow if they separately ask for implementation.
