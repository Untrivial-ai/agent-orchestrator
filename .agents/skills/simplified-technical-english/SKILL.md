---
name: simplified-technical-english
description: Write reviewer-facing and user-facing technical text for Agent Orchestrator in ASD-STE100 Simplified Technical English style. Use when drafting PR descriptions, commit message bodies, docs, runbooks, error messages, or any technical wording that must stay clear for non-native readers.
trigger: User drafts or edits a PR description, commit body, docs page, runbook, or error message, or asks for clear technical wording.
---

# Simplified Technical English (ASD-STE100)

Write clear, unambiguous technical English. Prefer STE writing rules over clever
or idiomatic phrasing.

This skill applies **STE writing rules** (grammar and style). It does **not**
embed the copyrighted STE dictionary (~900 approved words). Use simple words
with one clear meaning; use Agent Orchestrator **technical nouns** for product
terms. For full dictionary compliance, consult the official standard:
[ASD-STE100](https://asd-ste100.org/).

Source: ASD-STE100 Issue 9 (January 2025), Standard for technical documentation.
ASD owns the STE trademark and standard; this skill is an operational distillation
for agents, not a substitute for the official document.

## When this applies

| Text | STE mode |
| --- | --- |
| PR body (`What`, `Why`, `Testing`) | Descriptive + procedural |
| Commit message **body** | Descriptive |
| Docs, runbooks, onboarding steps | Procedural and/or descriptive |
| Error messages, confirmations, helper text | Procedural / short descriptive |

**Exceptions (do not rewrite these into STE):**

- Conventional Commits type prefixes (`feat:`, `fix:`, …)
- Code identifiers, API paths, table names, log lines, and quoted error codes
- Proper nouns and product names (`Agent Orchestrator`, `AO`, `GitHub`, `Electron`)
- Issue IDs and `Co-authored-by:` trailers

## Core rules (must follow)

### Words

1. Prefer plain words with **one meaning**. Do not use synonyms for the same idea
   in the same surface (`start` vs `begin` vs `initiate` — pick one and keep it).
2. Keep **one technical noun per concept**. Use Agent Orchestrator vocabulary
   consistently (see list below). Spell the product **Agent Orchestrator** or
   **AO**; do not invent variants.
3. Do not use slang, idioms, humor, or jargon that the reader cannot act on.
4. Use **American English** spelling.
5. Prefer verbs for actions. Do not hide actions in nouns
   (`Create a session`, not `Perform session creation`).
6. Keep multi-word technical nouns to **three words or fewer** when you invent
   a phrase (`session spawn step`, not `daemon session spawn lifecycle workflow step`).

### Verbs and voice

1. Prefer these forms: infinitive, **imperative**, simple present, simple past,
   simple future (`will`), past participle as adjective.
2. Avoid complex constructions: progressive (`is creating`), perfect
   (`has created`), and stacked auxiliaries when a simple form works.
3. Use the **active voice**. Use passive only in descriptive text when the actor
   is unknown.
4. Instructions and button labels use the **imperative** (`Start the daemon`,
   `Delete the session`).

### Sentences

1. Write short, clear sentences. Do not omit necessary words or use contractions
   to fake brevity (`do not`, not `don't`; `cannot`, not `can't`).
2. **Procedures / instructions / test steps:** maximum **20 words** per sentence.
   One instruction per sentence unless two actions occur at the same time.
3. **Descriptions** (PR `What`/`Why`, commit bodies, notes): maximum **25 words**
   per sentence.
4. One topic per paragraph; keep paragraphs short (about six sentences or fewer).
5. Use a vertical list when a sentence would list many items or steps.
6. Give information gradually: problem or goal first, then necessary detail.

### Procedural pattern

When the reader must do something (test plan, runbook steps, recovery text):

```text
If the condition is true, do the action.
```

- Put the condition first when the reader must know it before acting.
- Separate condition and command with a comma.
- Name the object and the outcome (`Delete "Fix login flow".`).

## Agent Orchestrator technical nouns (approved for PRs and docs)

Use these as stable technical nouns. Do not invent synonyms for the same thing:

- Agent Orchestrator
- AO
- daemon
- session
- project
- workspace
- worktree
- harness
- runtime
- lifecycle
- supervisor
- board
- chat
- observer
- reaper
- controller
- handoff

Code-only names (package paths, harness names, table names, port names) stay
out of prose unless the reader must act on them.

## Before / after

**Weak (not STE):**
`We've basically reworked how the onboarding bits kinda wire up GitHub login and stuff so it's nicer.`

**STE:**
`This change starts the GitHub login terminal automatically.`
`The new flow shows auth status and the next required step.`

**Weak PR test step:**
`Just make sure everything still works when you click around importing a workspace and then going back.`

**STE test step:**
`- [ ] Import a workspace from the onboarding screen.`
`- [ ] Confirm the board shows the new session.`
`- [ ] Go back one step and confirm the session stays ready.`

**Weak error:**
`Oops! Something went wrong on our end.`

**STE error:**
`AO could not connect to the daemon.`
`Check that the daemon runs on 127.0.0.1:3001 and try again.`

## Quality checklist

- [ ] Sentences stay within 20 words (instructions) or 25 words (descriptions)
- [ ] Active voice; imperative for commands
- [ ] No contractions, slang, idioms, or hype
- [ ] One term per concept; Agent Orchestrator vocabulary is consistent
- [ ] Lists used when steps or items would crowd a sentence
- [ ] PR/commit text explains **why** without narrative filler
- [ ] Error text states the action, object, and outcome

## Related skills

- PR titles, commit subjects, and PR structure:
  [commit-and-pr-messages](../commit-and-pr-messages/SKILL.md)
