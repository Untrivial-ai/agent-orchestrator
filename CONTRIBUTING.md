# Contributing

We love contributions — code, docs, triage, examples, and tests.
For implementation work, start on Discord so scope is clear before you invest time.
Bug reports can go straight to GitHub.

[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?style=for-the-badge&logo=discord&logoColor=white&logoSize=auto)](https://discord.com/invite/UZv7JjxbwG)

**Daily contributor sync:** every day at **10:00 PM IST**

- **Discord** → questions, mentoring, sync, realtime unblocking
- **GitHub** → bugs, proposals, design threads, review

Non-trivial work? Comment on the issue or ping Discord first. Get a thumbs-up, then build.

## Ways to contribute

| Type             | Examples                                       |
| ---------------- | ---------------------------------------------- |
| Code             | Fixes, features, adapters, performance         |
| Docs             | README, `docs/`, architecture notes            |
| Triage           | Repro bugs, tighten reports, label suggestions |
| Examples / tests | Recipes, edge cases, flaky-test hunts          |

## Quick start

1. **Join Discord** — say hi and get guidance
2. **Read the contract** — [AGENTS.md](AGENTS.md) (layout, commands, hard rules, PR hygiene)
3. **Pick something focused** — [open issues](https://github.com/AgentWrapper/agent-orchestrator/issues); prefer `good-first-issue` / `help wanted`
4. **Claim it** — comment `I'd like to work on this` and wait for assignment
5. **Open a clear PR** — narrow change, link the issue, user-visible impact, tests
6. **Iterate** — address review; maintainers merge

Need the product/run overview first? Start with [README.md](README.md),
[docs/architecture.md](docs/architecture.md), and
[docs/development.md](docs/development.md).

Two onboarding notes matter on current `main`:

- On fresh Linux setups, prefer `cd frontend && npm run package` unless you have also installed distro packaging tools such as `rpm`/`rpmbuild` for `npm run make`.
- Mobile companion app docs are still being filled in. Do not assume `packages/mobile/README.md` is a complete headless setup guide on this branch.

### Bugs and features

Use the [GitHub issue forms](https://github.com/Untrivial-ai/agent-orchestrator/issues/new/choose) for bugs and feature requests. For bugs, only a description is required. Write a short, concrete report in your own words: what you did and what went wrong. When available, add what you expected, steps to reproduce, how often it happens, AO version and OS, or screenshots/a recording. You can report an intermittent bug even if you cannot reproduce it reliably.

Search existing issues when possible; add your observations to a matching issue. Submit from your own GitHub account so maintainers can follow up with the person who experienced the problem. Please don't ask AO Bot to file reports on your behalf.

Agent assistance is welcome. We recommend asking your local coding agent to use the [bug-triage skill](.agents/skills/bug-triage/SKILL.md) to ask useful follow-up questions and gather supporting evidence. The issue body should contain only observations you supplied, without invented steps, impact, or root-cause claims. Keep agent-collected logs, relevant database excerpts, and analysis in separate, clearly labeled attachments; remove secrets and unrelated personal data before sharing. Evidence and screenshots are optional, and can be added later.

For example, ask your local agent:

```text
Use .agents/skills/bug-triage/SKILL.md to help with this bug: <what I observed>.
Ask about missing details that would help, draft a concise report using only
what I tell you, and collect relevant evidence separately for me to attach.
```

Review the draft and attachments before submitting. A clear observation is useful on its own; no diagnosis or proposed fix is needed.

### Pull requests

New PRs are prefilled from [`.github/pull_request_template.md`](.github/pull_request_template.md).
Also follow **PR hygiene** in [AGENTS.md](AGENTS.md): branch from `main`, one issue per PR, conventional commits, explain intentional omissions, and keep CI green for the area you touched.

## Code of Conduct

Be respectful, constructive, and assume good intent. Report problems to maintainers via Discord DM.

Thanks for making agent-orchestrator better for the next person who shows up.
