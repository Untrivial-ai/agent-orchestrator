# Documentation coverage

Reviewed 3 October 2026. The public desktop guide uses [v0.13.3](https://github.com/Untrivial-ai/agent-orchestrator/releases/tag/v0.13.3), published 1 October 2026 UTC. Source checks used PR #6166 at `44c6ca8a1f8a9492101f6928033fa7aa830b162f`. The follow-up changes edit documentation, not product behavior. Contributor guides describe that checkout. Cloud availability also depends on the deployed service and the signed-in account.

## What was checked

| Area | Documentation | Evidence | Remaining check |
| --- | --- | --- | --- |
| Desktop installation | [Installation](../frontend/src/docs/content/installation.mdx) | Official release metadata, download URLs, current Homebrew recipe, and release checksums | Fresh installer and Homebrew install/upgrade were not run |
| Local projects and configuration | [Projects](../frontend/src/docs/content/configuration/projects.mdx), [CLI](../frontend/src/docs/content/cli.mdx), and the embedded project command reference | CLI decoder, daemon request validation, project settings, and focused reproduction of unknown-key and effort loss | Product fixes remain open in #6169 and #5954 |
| Agent capabilities and accounts | [Agent catalog](../frontend/src/docs/content/plugins/agents/index.mdx) and setup guides | Worker, Chat, and reviewer registries checked separately; Codex settings label checked against stable UI strings | Provider authentication and account switching were not exercised |
| Local workflows | [Dashboard](../frontend/src/docs/content/dashboard.mdx), [Examples](../frontend/src/docs/content/examples.mdx), and [Review loop](../frontend/src/docs/content/guides/review-loop.mdx) | Renderer controls and CLI/daemon handlers for Automations, Cues, browser profiles, reports, recovery, and reviews | Provider-specific runtime behavior remains unverified |
| Notifications | [Dashboard notifications](../frontend/src/docs/content/plugins/notifiers/dashboard.mdx) and [STATUS](STATUS.md) | `NotificationCenter.tsx` implements one feed with automatic acknowledgement of loaded unread items | Native toast delivery was not exercised |
| Mobile | [Connect Mobile](../frontend/src/docs/content/configuration/remote-access.mdx) | Desktop Connect Mobile component, daemon bridge, and phone source under `packages/mobile/`, including onboarding, QR v2 pairing, multiple desktops, and the incomplete Tailscale enablement path | [Real-device acceptance and screenshots, #6178](https://github.com/Untrivial-ai/agent-orchestrator/issues/6178) |
| Cloud sessions | [Cloud guide](../frontend/src/docs/content/guides/cloud.mdx) | Desktop auth, organization selection, project/task UI, Archive/Restore, and control-plane handlers | [Authenticated deployment walkthrough and account limits, #6179](https://github.com/Untrivial-ai/agent-orchestrator/issues/6179) |
| Cloud development and deployment | [Cloud development](cloud-development.md), [Cloud README](../cloud/README.md), and [deployment runbook](../cloud/docs/deployment.md) | Repository ownership, local scripts, deployment argument handling, and provider lifecycle source | No staging or production deployment was performed |
| Telemetry | [Product telemetry](telemetry.md), README, and public FAQ | PostHog sink's shared installation ID, GitHub username event/person fields, and enabled GeoIP setting | No production analytics account or stored event data was inspected |
| Website and translated introductions | README, translated READMEs, and public links | Canonical docs host and capability catalog links; current landing-page copy | [Landing-page companion change, #6180](https://github.com/Untrivial-ai/agent-orchestrator/issues/6180) |
| Human readability | All 47 public-manual MDX pages and repository prose in this refresh | Unslop review for clear steps, repeated sections, undefined terms, long paragraphs, and consistent headings | Runtime verification is separate from writing quality |

## Local verification

The follow-up passed the production docs build, including its TypeScript check. The export contains 50 HTML pages for 47 public content routes. All 1,499 internal links and anchors resolved. The export preserves content anchors from both the pre-PR manual and the previous PR export, including three restored historical anchors. A separate check found no missing targets among 217 relative links in changed repository Markdown files.

Both backend and Cloud embedded skill-asset test packages passed. `git diff --check` passed. The mobile package passed a clean dependency install, TypeScript check, and all 1,498 tests across 161 files on Node 20. The revised feedback guide rendered in the local browser preview, had no horizontal overflow at 390 pixels, and produced no captured browser console errors. Native product screenshots and the acceptance tests listed below remain open. `ao preview` could not run because this Codex chat has no `AO_SESSION_ID`; the docs preview used the in-app browser directly.

The public mobile guide uses stable **Pair Desktop** and identifies **Pair a machine** in newer builds. It records the current Tailscale setup limitation. Real-device pairing and secure remote access remain unverified.

The full local backend race run reported failures in unchanged runtime and authentication tests, including command-probe timeouts. It did not establish a passing full suite. Native OS jobs, container checks, and the remaining CI checks require separate results; the successful documentation and mobile checks do not cover them.

## Homebrew verification

The existing [AgentWrapper tap](https://github.com/AgentWrapper/homebrew-tap/blob/main/Casks/agent-orchestrator.rb) contains version `0.13.3`. Its arm64 and x64 SHA-256 values match the corresponding official release assets. The old GitHub release URL redirects to `Untrivial-ai/agent-orchestrator` and returned HTTP 200. A new tap name is not needed for this documentation change.

The cask declares `auto_updates true`; AO's in-app updater remains the normal update path. The optional Homebrew commands follow the [Homebrew upgrade documentation](https://docs.brew.sh/Manpage). This review did not install, upgrade, or remove the user's app through Homebrew.

## Open acceptance work

| Follow-up | Status | Evidence needed to close it |
| --- | --- | --- |
| [#6178: Mobile walkthrough](https://github.com/Untrivial-ai/agent-orchestrator/issues/6178) | Open | Real iOS and Android pairing on LAN and secure remote access; off-Wi-Fi reconnect; sleep, push, removal, rotation, and version-mismatch checks; screenshots without credentials |
| [#6179: Cloud walkthrough and limits](https://github.com/Untrivial-ai/agent-orchestrator/issues/6179) | Open | Authorized account journey through sign-in, repository/provider setup, task, files, terminal, review, Archive/Restore; deployed release and account-specific costs/limits |
| [#6180: Landing-page coverage](https://github.com/Untrivial-ai/agent-orchestrator/issues/6180) | Open in the public repository for coordination | Companion change in the website's owning repository and a check of deployed copy and links |
| [#6169: Unknown CLI configuration keys](https://github.com/Untrivial-ai/agent-orchestrator/issues/6169) | Open product fix | CLI rejects unsupported JSON keys before replacing configuration |
| [#5954: CLI effort preservation](https://github.com/Untrivial-ai/agent-orchestrator/issues/5954) | Open product fix | CLI input and output preserve `agentConfig.effort` |

A public Cloud health response was reported as HTTP 200 on 3 October 2026 in the PR's earlier verification. It does not prove an authenticated user journey. The isolated desktop can run locally, but native screen capture failed during this follow-up; product walkthrough screenshots remain pending. No pairing credential was generated or captured.

Keep these rows open until their results are recorded. A source check, successful build, or readable guide cannot substitute for a real-device or authenticated-service test.
