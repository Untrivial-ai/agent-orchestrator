# GLM Agent product-contract discovery

Date: 2026-09-17

Disposition: **`different-product-found`**

## Question and acceptance rule

This record asks whether “GLM Agent” identifies an official ZhipuAI/Z.ai coding-agent terminal product that Agent Orchestrator can launch and restore. Evidence qualifies only when both the product documentation and the distributed artifact are controlled by ZhipuAI/Z.ai. A model subscription, hosted agent API, desktop application, or community bridge does not establish a terminal-agent contract.

## Search matrix

| Candidate | Vendor-owned docs | Vendor-owned artifact | Executable | Product type | Disposition |
|---|---|---|---|---|---|
| GLM Agent | [Z.ai documentation index](https://docs.z.ai/llms.txt), searched for `GLM Agent`, `GLM`, `agent`, `CLI`, and Chinese equivalents; no product page found | none found in Z.ai docs, the `zai-org` and `zhipuai` GitHub organizations, npm, or PyPI | none | no independently documented product | **different-product-found**: the vendor product using adjacent wording is ZCode, not GLM Agent |
| GLM Coding Plan | [Developer documentation](https://docs.z.ai/devpack/overview.md) and [quick start](https://docs.z.ai/devpack/quick-start.md) | Z.ai subscription and API credentials | none; configuration is for existing tools | model/provider offering | not an AO harness |
| ZCode | [ZCode site](https://zcode.z.ai/en), [documentation](https://zcode.z.ai/en/docs), and [installation guide](https://zcode.z.ai/en/docs/install) | vendor CDN release `3.11.2` and [`zai-org/zcode-plugins`](https://github.com/zai-org/zcode-plugins) | signed desktop installers; no supported terminal executable documented | separately named desktop agentic-development environment | **separate design** under canonical identity `zcode` |

Searches included the English names “GLM Agent,” “GLM coding agent,” “ZCode,” and “Z Code,” and the Chinese terms “GLM 智能体,” “代码智能体,” “编码智能体,” and “智谱代码.” Canonical vendor pages are cited above; registry and organization searches were used only to test distribution ownership.

## Ownership and provenance

### GLM Coding Plan

The [Z.ai documentation index](https://docs.z.ai/llms.txt) describes GLM models as usable in existing coding tools including Claude Code, Kilo Code, Cline, OpenCode, and OpenClaw. Its Developer Pack pages describe model access and configuration for those clients. It does not identify a local coding CLI named GLM Agent, an installable artifact, or a command with that identity.

The same documentation index lists [GLM Slide/Poster Agent](https://docs.z.ai/guides/agents/slide.md), [Translation Agent](https://docs.z.ai/guides/agents/translation.md), and other hosted agent APIs. Those are HTTP services, not persistent local coding-agent processes that AO can supervise.

### ZCode

ZCode is official but differently named. The vendor-controlled [ZCode site](https://zcode.z.ai/en) calls it ZCode and distributes desktop builds. The [release page](https://zcode.z.ai/en/changelog) identifies version `3.11.2`; vendor CDN update metadata publishes SHA-512 integrity values for macOS, Linux, and Windows artifacts. The public [`zai-org/zcode-plugins`](https://github.com/zai-org/zcode-plugins) repository is owned by `zai-org`, licensed Apache-2.0, and describes itself as ZCode's official plugin marketplace.

These sources prove an official ZCode product and distribution, but the public ZCode documentation does not document a supported external coding-agent CLI contract. Desktop features or internal “headless” behavior mentioned in release notes do not establish a stable command-line interface for third-party supervisors.

### Rejected community leads

| Artifact | Publisher / organization | License | Why it is not official proof |
|---|---|---|---|
| [`glm-acp-agent` 1.10.0](https://www.npmjs.com/package/glm-acp-agent) | npm maintainer `stefandevo`; source under `stefandevo/glm-acp-agent` | Apache-2.0 | Community ACP agent using GLM Coding Plan models; not distributed or documented by Z.ai |
| [`@xinghai123/glm-acp-agent` 2.0.0](https://www.npmjs.com/package/@xinghai123/glm-acp-agent) | npm maintainer `xinghai123` | Apache-2.0 | Community wrapper; its claim to use a ZCode CLI cannot substitute for a vendor contract |
| [`zcode-acp-server` 0.41.0](https://www.npmjs.com/package/zcode-acp-server) | npm maintainer `william0wang`; source under `william0wang/zcode-acp` | Apache-2.0 | Community bridge to headless ZCode, not a vendor-owned distribution |
| unscoped npm package `zcode` 0.0.1 | unrelated public npm publisher; no Z.ai provenance | not stated in registry metadata | Name collision only |

No package named `glm-agent`, `@zhipuai/glm-agent`, `@z-ai/glm-agent`, or `@zai/glm-agent` was present in npm on the discovery date. No matching PyPI project was found. Registry absence alone is not proof, but it corroborates the absence of a vendor-documented distribution.

## GLM Agent capability conclusion

No GLM Agent fixture directory was created because there is no accepted official artifact to execute. Without an official process contract, all terminal acceptance criteria remain unsupported:

| Capability | Result | Reason |
|---|---|---|
| Worker/orchestrator launch | unsupported | no vendor-supported process or command |
| Deterministic initial task delivery | unsupported | no launch or protocol contract |
| Persistent standing instructions | unsupported | no documented append-only system/instruction channel |
| Model and permission configuration | unsupported | no CLI configuration surface |
| Native session ID and exact restore | unsupported | no session lifecycle contract |
| Durable activity events | unsupported | no hook or structured event contract |
| Installation and auth readiness | unsupported | no official binary installation or non-secret auth probe |
| Structured Chat | unsupported | no official ACP or equivalent protocol |

## AO acceptance decision

| AO requirement | Evidence | Result |
|---|---|---|
| Official stable terminal product | no vendor URL or pinned artifact exists for “GLM Agent” | fail |
| Worker/orchestrator | no command transcript can be produced | fail |
| Initial task | no flag or protocol fixture can be produced | fail |
| Standing instructions | no append channel is documented | fail |
| Model and permissions | no supported help or protocol fixture exists | fail |
| Native ID and exact restore | no two-process fixture can be produced | fail |
| Durable activity | no event fixture can be produced | fail |
| Install/auth readiness | only provider/API and differently named desktop-product documentation exists | fail |
| Structured Chat | community ACP bridges only | not offered |

**Decision:** do not register `glm-agent`. ZCode is a real, vendor-owned product under a distinct identity, so its possible integration starts with the separate design in `docs/superpowers/specs/2026-09-17-zcode-agent-adapter-design.md`. This discovery adds no AO harness, database identity, API enum, UI option, reviewer, or Chat driver.
