# Contributing

We love contributions — code, docs, triage, examples, and tests.
Start on Discord so scope is clear before you invest time.

[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?style=for-the-badge&logo=discord&logoColor=white&logoSize=auto)](https://discord.com/invite/UZv7JjxbwG)

**Daily contributor sync:** every day at **10:00 PM IST**

- **Discord** → questions, mentoring, sync, realtime unblocking
- **GitHub Discussions / Issues** → bugs, proposals, design threads, review (also the fallback if Discord is unreachable)

Non-trivial work? Comment on the issue or ping Discord first. Get a thumbs-up, then build.

### If the Discord invite fails

Some people see Discord’s **“Invite invalid or expired”** screen even though the published invite is still live. Common causes: network blocks, account restrictions, or client glitches — not an intentional ban on newcomers.

**Workarounds:**

1. Open the invite in a desktop browser while logged into Discord: https://discord.com/invite/UZv7JjxbwG
2. Try another network / VPN if Discord is blocked in your region
3. If you still cannot join, use **GitHub instead** — no Discord required:
   - [Discussions](https://github.com/Untrivial-ai/agent-orchestrator/discussions) for Q&A and community chat
   - [Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) for bugs and feature proposals

Please do **not** wait on Discord access to file a bug or open a PR.

## Ways to contribute

| Type             | Examples                                       |
| ---------------- | ---------------------------------------------- |
| Code             | Fixes, features, adapters, performance         |
| Docs             | README, `docs/`, architecture notes            |
| Triage           | Repro bugs, tighten reports, label suggestions |
| Examples / tests | Recipes, edge cases, flaky-test hunts          |

## Quick start

1. **Join Discord** (or GitHub Discussions if the invite fails) — say hi and get guidance
2. **Read the contract** — [AGENTS.md](AGENTS.md) (layout, commands, hard rules, PR hygiene)
3. **Pick something focused** — [open issues](https://github.com/Untrivial-ai/agent-orchestrator/issues); prefer `good-first-issue` / `help wanted`
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

Use the GitHub issue forms (**Bug report** / **Feature request**) so reports stay reproducible.
Bug reports should include AO version, environment, repro steps, and expected vs actual behavior.

### Pull requests

New PRs are prefilled from [`.github/pull_request_template.md`](.github/pull_request_template.md).
Also follow **PR hygiene** in [AGENTS.md](AGENTS.md): branch from `main`, one issue per PR, conventional commits, explain intentional omissions, and keep CI green for the area you touched.

## Code of Conduct

Be respectful, constructive, and assume good intent. Report problems to maintainers via Discord DM, or via a private GitHub security/advisory channel if Discord is unavailable.

Thanks for making agent-orchestrator better for the next person who shows up.
