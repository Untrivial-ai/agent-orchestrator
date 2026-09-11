# ao review

Manage AO code reviews of a worker's PR.

## Syntax

```
ao review <subcommand> [args] [flags]
```

## Subcommands

---

### ao review request

Request AO's configured reviewer for the current worker's PR. Inside a worker,
the session defaults to `AO_SESSION_ID`; the request is rejected inside an AO
reviewer session so reviewers cannot recursively spawn reviewers.

**Syntax:**
```
ao review request [worker-session-id] [--reviewer harness] [--model model] [--json]
```

The daemon records the originating worker, reviewer/model, PR URL, and exact
head SHA. Repeated or concurrent requests for an active pass reuse it. A model
override is rejected when the selected reviewer adapter cannot apply it.

### ao review status

Show the current-head AO review state and any findings for the current worker.

**Syntax:**
```
ao review status [worker-session-id] [--json]
```

```bash
# From a worker session: use the configured reviewer and model.
ao review request
ao review status

# Request an explicit second opinion after the current pass completes.
ao review request --reviewer codex --model gpt-5.6
```

---

### ao review submit

Record a reviewer's result for a worker's PR.

**Syntax:**
```
ao review submit [worker-session-id] [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--body string` | Review body: a path to a Markdown file, or `-` to read from stdin | - |
| `--review-id string` | Id of the GitHub PR review just posted (the `.id` from the `gh api` POST that created the review) | - |
| `--reviews string` | JSON review results array or object: a path, or `-` to read from stdin | - |
| `--run string` | Review run id | Required |
| `--session string` | Worker session id (or pass it as the positional argument) | - |
| `--verdict string` | Review verdict: `approved` or `changes_requested` | Required |

## Examples

```bash
# Submit an approved review for session mer-3
ao review submit mer-3 --run review-run-1 --verdict approved
```

```bash
# Submit a changes-requested review with a body from stdin
echo "Please fix the null check on line 42." | ao review submit --session mer-3 --run review-run-1 --verdict changes_requested --body -
```
