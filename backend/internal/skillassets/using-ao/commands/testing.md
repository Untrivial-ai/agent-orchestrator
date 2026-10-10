# ao testing

When asked to test or verify a PR in the real app, run:

```bash
ao testing start --pr <url>
```

AO snapshots the PR's base and head and starts one comparison worker. That
worker uses the testing skill and its session tools to test both revisions,
collect screenshots and clips, and report back in AO with a verdict and draft
review comments for the user. It never posts to GitHub.

The command returns the run, attempt and worker session IDs. Hand those IDs
back to the requester. Do not improvise app launches, manually start Electron,
or replace the comparison with a separate Playwright run. If the command is
blocked, report `ao report --needs-input --note "<exact reason>"`.

Use `ao testing evidence <attemptId>` to read retained evidence receipts and
`ao testing stop <attemptId>` to cancel an attempt and clean up its target.
Read `ao testing --help` and `ao testing start --help` for flags.
