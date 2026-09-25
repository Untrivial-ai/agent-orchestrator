# ao report

Persist a meaningful worker report for durable delivery to the active project
orchestrator.

## Syntax

```text
ao report <free-form-text>
ao report --checkpoint --note <text> [output flags]
ao report --needs-input --note <text> [output flags]
ao report --stuck --note <text> [output flags]
ao report --done --note <text> [output flags]
```

Output flags are repeatable:

```text
--artifact <opaque-reference>
--pr-created <github-pr-url>
--pr-reviewed <github-pr-url>
```

Use reports for meaningful transitions, decisions, blockers, required input,
outputs, and terminal judgment. Do not narrate routine commands. Outputs do not
imply completion, and `--done` does not terminate the session.

Report any artifact as soon as it exists, not only at `--done`: attach it with
`--artifact <reference>` on the report for the milestone that produced it. An
artifact is anything durable the orchestrator or human should be able to open
directly — a published Claude Artifact link, a generated document, a rendered
dashboard, or similar output. `--artifact` takes any opaque reference string;
it is not validated as a URL.

`--needs-input` requests immediate non-interrupting delivery. `--stuck`
requests immediate delivery plus a rate-limited interrupt. Informational work
batches for up to one hour, while the first done report opens a fixed five
minute settlement window.
