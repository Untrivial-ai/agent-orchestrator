<div align="center">
  <img src="assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### Plan, run, and supervise coding agents from one place.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Give every coding task its own agent, workspace, and feedback loop.<br />
Plan and delegate larger outcomes with a project-aware orchestrator.<br />
Follow every worker, pull request, CI run, and review in a live Kanban.

[**Download AO**](#install) &nbsp;&bull;&nbsp; [Documentation](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [Releases](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Contributing](CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

**English** · [简体中文](translations/README.zh-CN.md) · [日本語](translations/README.ja.md) · [한국어](translations/README.ko.md) · [Español](translations/README.es.md) · [Français](translations/README.fr.md) · [Deutsch](translations/README.de.md) · [Português (Brasil)](translations/README.pt-BR.md)

<br />

<img src="docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing worker sessions grouped by live status" width="100%" />
</div>

## A workspace for agent-driven development

One coding agent can handle a task. Running several across a project creates a different job: deciding what matters, splitting work cleanly, giving each agent the right context, preventing branch collisions, and following every change through review and merge.

AO is a local desktop workspace built for that job. Add a repository and create a worker session with the coding agent, model, and interface that fit the task. For Git-backed work, AO gives the worker its own branch and worktree. The task, conversation, terminal, changed files, browser preview, pull request, CI, and review state stay attached to that session from start to finish.

Behind the desktop app, AO's local daemon watches agent activity and source-control state. The result is a shared, live view of the project instead of a collection of disconnected terminals, branches, and browser tabs.

<img src="docs/assets/readme/tui.png" alt="Agent Orchestrator workspace showing a coding agent's native terminal UI" width="100%" />

## Workers execute focused tasks

A worker is AO's unit of execution: one task, one coding agent, and one isolated workspace. Use **New task** when the work is already clear. Describe the outcome, choose an agent and model, attach relevant files, and work with the agent in structured Chat or its native terminal UI.

Open a worker at any time to continue the conversation, attach to its terminal, inspect its changes, use its isolated browser, review its pull request, or send CI and review feedback back to the same agent. This makes each task independently understandable and keeps parallel work from collapsing into one shared context.

<img src="docs/assets/readme/new-task.png" alt="Create a new task in Agent Orchestrator with an agent and model selected" width="100%" />

## The orchestrator plans across the project

The project orchestrator is AO's persistent planning and coordination agent. It works at the level above individual tasks: the product direction, technical strategy, priorities, and sequence of work across the repository.

Use the orchestrator to explore an idea before implementation, brainstorm product and technical approaches, reason through tradeoffs, identify high-impact work, and turn an ambiguous outcome into a concrete plan. Its project-scoped conversation preserves goals, decisions, constraints, and earlier reasoning. It combines that planning history with repository context and live AO state, including active workers, ownership, pull requests, CI, and reviews. This keeps planning grounded in both the project and the work already underway.

When a plan becomes actionable, the orchestrator can break it into focused tasks, spawn or redirect workers, pass each worker the relevant context, follow their progress, and coordinate follow-up work. The orchestrator owns planning and delegation; workers own implementation, tests, commits, and pull requests.

<img src="docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers and passing them focused project context" width="100%" />

## The Kanban keeps the system legible

Every worker appears on the same live board, whether you started it from **New task** or the orchestrator delegated it. AO derives each card's position from session, pull request, CI, and review facts, turning the Kanban into an operational view of the project:

- **Working:** workers that are actively implementing or ready for another instruction
- **Needs you:** blocked sessions, missing input, failed CI, requested changes, or lost signals
- **In review:** open and draft pull requests waiting on checks or review
- **Ready to merge:** approved or mergeable work, with merged sessions kept visible until they are archived

Each card keeps the task, agent, branch, activity, pull request, and status together. Open it to inspect the conversation or terminal, changed files, PR summary, reviews, and preview. The board shows what is moving, what is blocked, and where your attention will have the most impact.

<img src="docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing worker sessions grouped by live status" width="100%" />

## One workflow, from idea to merge

1. **Start at the right level.** Give a clear task directly to a worker, or develop a larger outcome with the project orchestrator and let it shape the plan.
2. **Delegate focused work.** Start workers yourself or have the orchestrator create them with the context and ownership they need.
3. **Build in isolation.** Every Git-backed worker gets its own branch and worktree; Scratch workers get AO-managed branchless directories.
4. **Supervise live state.** AO follows agent activity, pull requests, CI, review feedback, and merge conflicts, then reflects those facts on the Kanban.
5. **Close the feedback loop.** Inspect any worker directly, make project-level decisions with the orchestrator, and return actionable failures or review comments to the agent that owns the work.

AO works with the coding agents and source-control workflow you already use. Agents keep their native strengths; AO supplies the project context, isolated execution, coordination, and operational view that make them work as a system.

## Product highlights

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests and agent reviews</h3>
      <p>Keep CI, mergeability, reviewer state, and interactive agent reviews beside the worker, then return requested changes to the same owner.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/review.png" alt="Worker session with pull request, CI, and agent review state in Agent Orchestrator" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Agent-controllable browser</h3>
      <p>Preview and inspect a worker's local app beside its interface. Browser profiles are isolated per worker so parallel UI tasks do not share state.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/browser.png" alt="A worker controlling its isolated in-app browser preview" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Native interfaces, one supervisor</h3>
      <p>Use structured Chat or the agent's native terminal UI while AO keeps task context, workspace state, and feedback in one place.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## Supported agents

**26 coding agents supported** through one supervised workflow.

<table>
  <tr valign="middle">
    <td width="33%" valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/claude-code.svg" alt="Claude Code" width="24" height="24" align="middle" /> &nbsp; <b>Claude Code</b></td>
    <td width="33%" valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/codex.svg" alt="Codex" width="24" height="24" align="middle" /> &nbsp; <b>Codex</b></td>
    <td width="33%" valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/cursor.svg" alt="Cursor" width="24" height="24" align="middle" /> &nbsp; <b>Cursor</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/opencode.svg" alt="opencode" width="24" height="24" align="middle" /> &nbsp; <b>opencode</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/aider.png" alt="Aider" width="24" height="24" align="middle" /> &nbsp; <b>Aider</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/copilot.svg" alt="GitHub Copilot" width="24" height="24" align="middle" /> &nbsp; <b>GitHub Copilot</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/grok.png" alt="Grok" width="24" height="24" align="middle" /> &nbsp; <b>Grok</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/kimi.png" alt="Kimi" width="24" height="24" align="middle" /> &nbsp; <b>Kimi</b></td>
    <td valign="middle" nowrap><img src="docs/assets/readme/agents/pi-coding-agent.svg" alt="Pi" width="24" height="24" align="middle" /> &nbsp; <b>Pi</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/amp.svg" alt="Amp" width="24" height="24" align="middle" /> &nbsp; <b>Amp</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/auggie.svg" alt="Auggie" width="24" height="24" align="middle" /> &nbsp; <b>Auggie</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/droid.png" alt="Droid" width="24" height="24" align="middle" /> &nbsp; <b>Droid</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/crush.png" alt="Crush" width="24" height="24" align="middle" /> &nbsp; <b>Crush</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/cline.svg" alt="Cline" width="24" height="24" align="middle" /> &nbsp; <b>Cline</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/goose.svg" alt="Goose" width="24" height="24" align="middle" /> &nbsp; <b>Goose</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/qwen.png" alt="Qwen" width="24" height="24" align="middle" /> &nbsp; <b>Qwen</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/continue.png" alt="Continue" width="24" height="24" align="middle" /> &nbsp; <b>Continue</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/devin.png" alt="Devin" width="24" height="24" align="middle" /> &nbsp; <b>Devin</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/kiro.png" alt="Kiro" width="24" height="24" align="middle" /> &nbsp; <b>Kiro</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/kilocode.svg" alt="Kilo Code" width="24" height="24" align="middle" /> &nbsp; <b>Kilo Code</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/vibe.png" alt="Vibe" width="24" height="24" align="middle" /> &nbsp; <b>Vibe</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/muse.png" alt="Muse" width="24" height="24" align="middle" /> &nbsp; <b>Muse</b></td>
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/agy.png" alt="Agy" width="24" height="24" align="middle" /> &nbsp; <b>Agy</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/agents/autohand-stacked-dark.png" /><img src="docs/assets/readme/agents/autohand-stacked-light.png" alt="Autohand" width="24" height="24" align="middle" /></picture> <b>Autohand</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="frontend/src/renderer/assets/agents/kimchi.svg" alt="Kimchi" width="24" height="24" align="middle" /> &nbsp; <b>Kimchi</b></td>
    <td valign="middle" nowrap><img src="docs/assets/readme/agents/prime-agent.svg" alt="Prime Agent" width="24" height="24" align="middle" /> &nbsp; <b>Prime Agent</b></td>
    <td valign="middle" nowrap></td>
  </tr>
</table>

[Browse agent setup guides →](https://aoagents.dev/docs/plugins/agents)

**Use the interface that fits the moment: structured Chat or the agent's native terminal UI.**

## Install

Download the latest AO desktop app for your platform. AO checks for updates automatically.

| Platform              | Download                                                                                                                      |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Open Agent Orchestrator and point it at the repository you want AO to manage. The desktop app runs the daemon for you, so no CLI is required. See the [installation guide](https://aoagents.dev/docs/installation) for agent CLI setup and troubleshooting.

## Report a bug

The recommended way to report a bug is to ask your coding agent to follow the repository's [bug-triage skill](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md). It guides the agent through reproducing the problem on current code, gathering diagnostics, tracing the relevant code path, checking for duplicates, and filing or updating a detailed GitHub issue.

Whether you ask a local coding agent or AO Bot on Discord, attach screenshots and share as much relevant context as possible. Include what happened, where and when it happened, steps to reproduce it, your OS and AO version, and whether the problem is consistent or intermittent. This gives the agent the best chance of reproducing the bug and filing an actionable report.

```text
Read and follow https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Please reproduce and triage this bug, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

You can also report a bug in the [bug-triaging channel on Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Tag `@AO Bot#8425`, describe what happened, and ask it to use the bug-triage skill.

```text
@AO Bot#8425 Please reproduce and triage this bug using the bug-triage skill, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

## Develop and contribute

Contributions are welcome across code, docs, triage, examples, and tests.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

Start with the [development guide](docs/development.md) for prerequisites, local setup, and test commands. Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request, and use [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) for bugs and feature requests.

## Documentation

| Document                                                         | Start here when you need                                                                     |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| [Product documentation](https://aoagents.dev/docs)               | Installation, agent setup, and day-to-day product usage.                                     |
| [docs/architecture.md](docs/architecture.md)                     | Backend mental model, lifecycle, persistence, CDC, status derivation, and daemon boundaries. |
| [docs/backend-code-structure.md](docs/backend-code-structure.md) | Package ownership and where each backend concern belongs.                                    |
| [docs/cli/README.md](docs/cli/README.md)                         | CLI behavior and daemon route mapping.                                                       |
| [docs/development.md](docs/development.md)                       | Prerequisites, build steps, running tests, and troubleshooting for local development.        |
| [docs/STATUS.md](docs/STATUS.md)                                 | What currently ships on `main` and what remains in flight.                                   |

## Follow the journey

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="assets/tweet2.png" height="330" alt="Agent Orchestrator journey update on X" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="assets/tweet1.png" height="330" alt="Agent Orchestrator journey update on X" />
      </a>
    </td>
  </tr>
</table>

## Community

Join [Discord](https://discord.com/invite/UZv7JjxbwG) for help and contributor discussion, follow [@aoagents](https://x.com/aoagents) for updates, or start a conversation in [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues).

## Anonymous telemetry

AO uses privacy-preserving product usage and reliability metrics designed to exclude PII and project content. These metrics help us understand adoption and improve the product. [Learn more about telemetry and privacy](docs/telemetry.md).

## License

Agent Orchestrator is available under the [Apache License 2.0](LICENSE).
