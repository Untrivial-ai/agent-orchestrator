---
name: commit-and-pr-messages
description: Write Git commit messages and pull request titles/descriptions for Agent Orchestrator using Chris Beams / Tim Pope conventions (subject/body split, ~50-char subject, imperative mood, why-not-how body) plus Simplified Technical English for bodies and PR text. Use when creating commits, drafting or editing PR descriptions, writing PR titles, or when the user asks for help with commit/PR wording or git history style.
trigger: User creates or amends a commit message, opens or updates a pull request, or asks for help with commit/PR wording.
---

# Commit and PR Messages

Write history that future readers can scan, search, and trust. A diff shows
*what* changed; the message must explain *why*.

Based on [How to Write a Git Commit Message](https://cbea.ms/git-commit/)
(Chris Beams) and [A Note About Git Commit Messages](https://tbaggery.com/2008/04/19/a-note-about-git-commit-messages.html)
(Tim Pope), adapted for Agent Orchestrator's Conventional Commits and PR hygiene.

**Language:** Write commit bodies and PR text in ASD-STE100 Simplified
Technical English style. Follow
[simplified-technical-english](../simplified-technical-english/SKILL.md)
before you finalize wording.

## When to use

- Creating or amending a commit message
- Opening or updating a pull request (title and body)
- Reviewing whether a PR description is useful to reviewers

## Commit messages — seven rules

1. **Separate subject from body with a blank line.**
2. **Limit the subject line to ~50 characters** (72 hard max). Use the
   Conventional Commits shape this repo uses:
   `type(scope): Imperative summary`. Keep the whole subject readable.
3. **Capitalize the subject line** after the type prefix
   (`fix(board): Stop spinner on settled session cards`).
4. **Do not end the subject line with a period.**
5. **Use the imperative mood in the subject line** — as if completing:
   *If applied, this commit will ________.*
   Prefer `Fix`, `Add`, `Remove`, `Refactor` over `Fixed`, `Adds`, `Fixing`.
6. **Wrap the body at 72 characters.**
7. **Use the body to explain what and why, not how.** The code shows how.
   Cover motivation, prior behavior, trade-offs, and non-obvious side effects.

### Model commit

```text
Capitalized, short (50 chars or less) summary

More detailed explanatory text, if necessary. Wrap it to about 72
characters or so. The blank line after the subject is mandatory when a
body is present.

Explain the problem this commit solves and why this approach was chosen.
Leave mechanical how-details to the diff unless the approach is surprising.

Further paragraphs come after blank lines.

- Bullet points are okay

Fixes #123
```

### Agent Orchestrator commit extras

- Use the types from [AGENTS.md](../../../AGENTS.md): `feat:`, `fix:`,
  `docs:`, `test:`, `chore:`. Add a scope for the area when it helps:
  `fix(onboarding): …`, `fix(board): …`, `fix(desktop): …`,
  `fix(mobile): …`, `fix(chat): …`, `feat(board): …`.
- **Do not add a DCO `Signed-off-by` trailer.** This repo does not use DCO.
  Preserve `Co-authored-by:` trailers when co-authors exist.
- Put issue references at the end of the body (`Fixes #123`, `Closes #123`).
  Do not stuff them into the subject and do not invent ticket IDs.
- Keep one logical change per commit when practical. If the subject is hard
  to write, the commit may be doing too much.
- Squash context: GitHub appends `(#NNNN)` to the squash-commit subject from
  the PR number. Do not hand-write `(#NNNN)` in the PR title field.

## Pull request titles

PR titles are the public subject line for a whole change set, and they become
the squash-commit subject.

- Start with a Conventional Commits prefix: `feat:`, `fix:`, `docs:`,
  `test:`, or `chore:`, plus a scope when it helps (`fix(board): …`).
- After the prefix, write an imperative, capitalized summary with no trailing
  period — same mood test as commits.
- Keep the title scannable; put nuance in the body, not a novel in the title.

**Good:** `fix(board): Stop spinner on settled session cards`
**Good:** `feat: Create GitHub repository during project import`
**Weak:** `fix: Updated some stuff for onboarding`
**Weak:** `Fixed the bug.`

## Pull request descriptions

New PRs are prefilled from `.github/pull_request_template.md`. Fill that
template; do not replace it with another shape. Lead with purpose; do not
narrate the diff file-by-file. Write `Why` and `Testing` in STE
([simplified-technical-english](../simplified-technical-english/SKILL.md)).

### Required shape (the repo template)

```markdown
## What

<1–3 STE sentences: the change in present orientation.>

## Why

<Problem this solves and motivation. Reference the issue, for example
`Fixes #123`. State before/after behavior and impact.>

## How

<Key implementation decisions, trade-offs, and reviewer focus areas.
Call out intentional omissions, especially versus older TypeScript
behavior, when relevant. This is the only section for approach detail.>

## Testing

<How you validated the change: commands, scenarios, screenshots for UI.
Use STE procedural steps: max 20 words, one imperative instruction each.>

## Checklist

<Keep the template checklist current: branch base, focus, conventions,
tests, CI for the area touched.>
```

### Writing rules

- **STE first.** Short sentences, active voice, no contractions, no slang.
- **Why over how.** State the before/after behavior and the reason for the
  change outside `How`. Mention approach only in `How`, and only what a
  reviewer needs.
- **Present / imperative orientation.** Describe what the PR *does*
  (`Adds…`, `Fixes…`, `Removes…`), not a diary of yesterday's work.
- **Honest scope.** Call out follow-ups, known gaps, and intentional
  non-goals. Per [AGENTS.md](../../../AGENTS.md) PR hygiene, explain
  intentional omissions versus the old TypeScript behavior when relevant.
- **One issue per PR.** Branch from `main` unless continuing an existing PR
  branch. Link the related issue when applicable.
- **Scannable.** Short paragraphs or bullets; no wall of implementation notes
  that duplicates the diff.
- **Link context.** Reference issues (`Fixes #123`) when they exist; do not
  invent ticket IDs.

### Avoid

- Subject-only PRs with an empty or placeholder body when the change needs
  context
- Restating every file touched (`Updated Foo.go, Bar.tsx, …`)
- Past-tense changelog dumping with no problem statement
- Marketing or hype language

### Quick quality check

Before submitting a PR description, confirm:

- [ ] Title completes: *If merged, this PR will ________.*
- [ ] `Why` explains **why**, not a file list
- [ ] `How` covers decisions, trade-offs, and intentional omissions
- [ ] `Why` and `Testing` obey STE sentence limits and style
- [ ] `Testing` has actionable checks (commands, scenarios, screenshots for UI)
- [ ] No trailing period / non-imperative mush in the title
- [ ] Body still makes sense months later without the author present
- [ ] [AGENTS.md](../../../AGENTS.md) PR hygiene holds: correct base,
      one focused change, conventions followed, tests added where useful,
      relevant CI checks addressed
