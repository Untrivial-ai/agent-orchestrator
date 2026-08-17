<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### 한곳에서 코딩 에이전트의 작업을 계획하고 실행하고 감독하세요.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

모든 코딩 작업에 전용 에이전트, 워크스페이스, 피드백 루프를 제공하세요.<br />
프로젝트를 이해하는 오케스트레이터와 더 큰 목표를 계획하고 위임하세요.<br />
모든 워커, 풀 리퀘스트, CI 실행, 리뷰를 실시간 Kanban에서 확인하세요.

[**AO 다운로드**](#설치) &nbsp;&bull;&nbsp; [문서](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [릴리스](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [기여하기](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · **한국어** · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="워커 세션을 실시간 상태별로 보여주는 Agent Orchestrator Kanban" width="100%" />
</div>

## 에이전트 중심 개발을 위한 워크스페이스

코딩 에이전트 하나는 작업 하나를 처리할 수 있습니다. 하지만 프로젝트에서 여러 에이전트를 동시에 실행하면 전혀 다른 일이 생깁니다. 무엇이 중요한지 결정하고, 작업을 명확히 나누고, 각 에이전트에 적절한 컨텍스트를 제공하고, 브랜치 충돌을 막고, 모든 변경 사항을 리뷰부터 머지까지 추적해야 합니다.

AO는 이 일을 위해 만든 로컬 데스크톱 워크스페이스입니다. 저장소를 추가하고 작업에 맞는 코딩 에이전트, 모델, 인터페이스로 워커 세션을 만드세요. Git 기반 작업에서는 AO가 워커 전용 브랜치와 worktree를 제공합니다. 작업, 대화, 터미널, 변경된 파일, 브라우저 미리보기, 풀 리퀘스트, CI, 리뷰 상태가 처음부터 끝까지 해당 세션에 연결되어 유지됩니다.

데스크톱 앱 뒤에서는 AO의 로컬 데몬이 에이전트 활동과 소스 제어 상태를 감시합니다. 그 결과, 서로 분리된 터미널과 브랜치, 브라우저 탭의 모음이 아니라 프로젝트 전체를 하나의 공유 실시간 뷰에서 볼 수 있습니다.

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## 워커가 명확한 작업을 실행합니다

워커는 AO의 실행 단위입니다. 하나의 작업, 하나의 코딩 에이전트, 하나의 격리된 워크스페이스로 구성됩니다. 해야 할 일이 이미 명확하다면 **New task**를 사용하세요. 원하는 결과를 설명하고, 에이전트와 모델을 선택하고, 관련 파일을 첨부한 뒤 구조화된 Chat 또는 에이전트 고유의 터미널 UI에서 작업할 수 있습니다.

언제든 워커를 열어 대화를 계속하거나, 터미널에 연결하거나, 변경 사항을 살펴보거나, 격리된 브라우저를 사용하거나, 풀 리퀘스트를 검토하거나, CI 및 리뷰 피드백을 같은 에이전트에게 돌려보낼 수 있습니다. 덕분에 각 작업을 독립적으로 이해할 수 있고, 병렬 작업이 하나의 공유 컨텍스트에 뒤섞이지 않습니다.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## 오케스트레이터가 프로젝트 전체를 계획합니다

프로젝트 오케스트레이터는 AO의 지속적인 계획 및 조정 에이전트입니다. 개별 작업보다 한 단계 높은 수준에서 제품 방향, 기술 전략, 우선순위, 저장소 전반의 작업 순서를 다룹니다.

구현 전에 아이디어를 탐색하고, 제품 및 기술 접근 방식을 브레인스토밍하고, 트레이드오프를 검토하고, 영향력이 큰 작업을 찾고, 모호한 목표를 구체적인 계획으로 바꾸는 데 오케스트레이터를 사용할 수 있습니다. 프로젝트 단위의 대화에는 목표, 결정, 제약 조건, 이전 논의가 계속 보존됩니다. 오케스트레이터는 이 계획 기록을 저장소 컨텍스트 및 AO의 실시간 상태와 결합합니다. 여기에는 활성 워커, 담당 범위, 풀 리퀘스트, CI, 리뷰가 포함됩니다. 따라서 계획은 프로젝트 자체와 이미 진행 중인 작업 모두에 기반합니다.

계획이 실행 가능한 상태가 되면 오케스트레이터는 이를 명확한 작업으로 나누고, 워커를 생성하거나 방향을 전환하고, 각 워커에 필요한 컨텍스트를 전달하고, 진행 상황을 추적하고, 후속 작업을 조정할 수 있습니다. 오케스트레이터는 계획과 위임을 맡고, 워커는 구현, 테스트, 커밋, 풀 리퀘스트를 맡습니다.

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## Kanban이 전체 시스템을 한눈에 보여줍니다

**New task**에서 직접 시작했든 오케스트레이터가 위임했든, 모든 워커가 같은 실시간 보드에 표시됩니다. AO는 세션, 풀 리퀘스트, CI, 리뷰의 사실을 바탕으로 각 카드의 위치를 결정하여 Kanban을 프로젝트의 운영 뷰로 만듭니다.

- **Working:** 현재 구현 중이거나 다음 지시를 받을 준비가 된 워커
- **Needs you:** 차단됨, 입력 필요, CI 실패, 변경 요청 또는 신호 끊김 상태인 세션
- **In review:** 검사나 리뷰를 기다리는 열려 있거나 초안 상태인 풀 리퀘스트
- **Ready to merge:** 승인되었거나 머지할 수 있는 작업. 머지된 세션도 보관 처리할 때까지 계속 표시됩니다

각 카드에는 작업, 에이전트, 브랜치, 활동, 풀 리퀘스트, 상태가 함께 표시됩니다. 카드를 열어 대화 또는 터미널, 변경된 파일, PR 요약, 리뷰, 미리보기를 확인할 수 있습니다. 보드는 무엇이 진행 중인지, 무엇이 막혔는지, 어디에 집중해야 가장 큰 효과를 낼 수 있는지 보여줍니다.

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## 아이디어부터 머지까지 하나의 워크플로로

1. **알맞은 수준에서 시작하세요.** 명확한 작업은 워커에게 직접 맡기고, 더 큰 목표는 프로젝트 오케스트레이터와 구체화하여 계획을 세우세요.
2. **작업을 명확히 나눠 위임하세요.** 직접 워커를 시작하거나, 오케스트레이터가 필요한 컨텍스트와 담당 범위를 갖춘 워커를 만들도록 하세요.
3. **격리된 환경에서 개발하세요.** 모든 Git 기반 워커는 전용 브랜치와 worktree를 사용합니다. Scratch 워커는 AO가 관리하는 브랜치 없는 디렉터리를 사용합니다.
4. **실시간 상태를 감독하세요.** AO는 에이전트 활동, 풀 리퀘스트, CI, 리뷰 피드백, 머지 충돌을 추적하고 그 사실을 Kanban에 반영합니다.
5. **피드백 루프를 완성하세요.** 워커를 직접 살펴보고, 오케스트레이터와 프로젝트 수준의 결정을 내리고, 조치 가능한 실패나 리뷰 코멘트를 해당 작업을 맡은 에이전트에게 돌려보내세요.

AO는 이미 사용 중인 코딩 에이전트와 소스 제어 워크플로와 함께 작동합니다. 에이전트는 각자의 고유한 강점을 유지하고, AO는 이들이 하나의 시스템으로 작동하는 데 필요한 프로젝트 컨텍스트, 격리된 실행 환경, 조정 기능, 운영 뷰를 제공합니다.

## 제품 주요 기능

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>풀 리퀘스트와 에이전트 리뷰</h3>
      <p>CI, 머지 가능 여부, 리뷰어 상태, 대화형 에이전트 리뷰를 워커 옆에서 확인하고 요청된 변경 사항을 같은 담당 에이전트에게 돌려보내세요.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Agent Orchestrator에서 풀 리퀘스트, CI, 에이전트 리뷰 상태를 보여주는 워커 세션" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>에이전트가 제어할 수 있는 브라우저</h3>
      <p>워커 인터페이스 옆에서 로컬 앱을 미리 보고 검사하세요. 브라우저 프로필은 워커별로 격리되므로 병렬 UI 작업 사이에 상태가 공유되지 않습니다.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="격리된 인앱 브라우저 미리보기를 제어하는 워커" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>네이티브 인터페이스를 하나의 감독 화면에서</h3>
      <p>구조화된 Chat 또는 에이전트의 네이티브 터미널 UI를 사용하면서 AO가 작업 컨텍스트, 워크스페이스 상태, 피드백을 한곳에서 관리합니다.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## 지원 에이전트

하나의 감독된 워크플로에서 **26개의 코딩 에이전트**를 지원합니다.

<table>
  <tr valign="middle">
    <td width="33%" valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/claude-code.svg" alt="Claude Code" width="24" height="24" align="middle" /> &nbsp; <b>Claude Code</b></td>
    <td width="33%" valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/codex.svg" alt="Codex" width="24" height="24" align="middle" /> &nbsp; <b>Codex</b></td>
    <td width="33%" valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/cursor.svg" alt="Cursor" width="24" height="24" align="middle" /> &nbsp; <b>Cursor</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/opencode.svg" alt="opencode" width="24" height="24" align="middle" /> &nbsp; <b>opencode</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/aider.png" alt="Aider" width="24" height="24" align="middle" /> &nbsp; <b>Aider</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/copilot.svg" alt="GitHub Copilot" width="24" height="24" align="middle" /> &nbsp; <b>GitHub Copilot</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/grok.png" alt="Grok" width="24" height="24" align="middle" /> &nbsp; <b>Grok</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kimi.png" alt="Kimi" width="24" height="24" align="middle" /> &nbsp; <b>Kimi</b></td>
    <td valign="middle" nowrap><img src="../docs/assets/readme/agents/pi-coding-agent.svg" alt="Pi" width="24" height="24" align="middle" /> &nbsp; <b>Pi</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/amp.svg" alt="Amp" width="24" height="24" align="middle" /> &nbsp; <b>Amp</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/auggie.svg" alt="Auggie" width="24" height="24" align="middle" /> &nbsp; <b>Auggie</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/droid.png" alt="Droid" width="24" height="24" align="middle" /> &nbsp; <b>Droid</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/crush.png" alt="Crush" width="24" height="24" align="middle" /> &nbsp; <b>Crush</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/cline.svg" alt="Cline" width="24" height="24" align="middle" /> &nbsp; <b>Cline</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/goose.svg" alt="Goose" width="24" height="24" align="middle" /> &nbsp; <b>Goose</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/qwen.png" alt="Qwen" width="24" height="24" align="middle" /> &nbsp; <b>Qwen</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/continue.png" alt="Continue" width="24" height="24" align="middle" /> &nbsp; <b>Continue</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/devin.png" alt="Devin" width="24" height="24" align="middle" /> &nbsp; <b>Devin</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kiro.png" alt="Kiro" width="24" height="24" align="middle" /> &nbsp; <b>Kiro</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kilocode.svg" alt="Kilo Code" width="24" height="24" align="middle" /> &nbsp; <b>Kilo Code</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/vibe.png" alt="Vibe" width="24" height="24" align="middle" /> &nbsp; <b>Vibe</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/muse.png" alt="Muse" width="24" height="24" align="middle" /> &nbsp; <b>Muse</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/agy.png" alt="Agy" width="24" height="24" align="middle" /> &nbsp; <b>Agy</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../docs/assets/readme/agents/autohand-stacked-dark.png" /><img src="../docs/assets/readme/agents/autohand-stacked-light.png" alt="Autohand" width="24" height="24" align="middle" /></picture> <b>Autohand</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kimchi.svg" alt="Kimchi" width="24" height="24" align="middle" /> &nbsp; <b>Kimchi</b></td>
    <td valign="middle" nowrap><img src="../docs/assets/readme/agents/prime-agent.svg" alt="Prime Agent" width="24" height="24" align="middle" /> &nbsp; <b>Prime Agent</b></td>
    <td valign="middle" nowrap></td>
  </tr>
</table>

[에이전트 설정 가이드 살펴보기 →](https://aoagents.dev/docs/plugins/agents)

**상황에 맞는 인터페이스를 사용하세요. 구조화된 Chat과 에이전트 고유의 터미널 UI를 모두 지원합니다.**

## 설치

사용 중인 플랫폼에 맞는 최신 AO 데스크톱 앱을 다운로드하세요. AO는 업데이트를 자동으로 확인합니다.

| 플랫폼                | 다운로드                                                                                                                      |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [다운로드](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Agent Orchestrator를 열고 AO가 관리할 저장소를 지정하세요. 데스크톱 앱이 데몬을 실행하므로 CLI는 필요하지 않습니다. 에이전트 CLI 설정 및 문제 해결은 [설치 가이드](https://aoagents.dev/docs/installation)를 참고하세요.

## 버그 신고

버그를 신고할 때는 코딩 에이전트에게 이 저장소의 [bug-triage skill](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md)을 따르도록 요청하는 방법을 권장합니다. 이 skill은 에이전트가 현재 코드에서 문제를 재현하고, 진단 정보를 수집하고, 관련 코드 경로를 추적하고, 중복 이슈를 확인한 뒤, 상세한 GitHub 이슈를 생성하거나 업데이트하도록 안내합니다.

로컬 코딩 에이전트나 Discord의 AO Bot 중 어느 쪽에 요청하더라도 스크린샷을 첨부하고 가능한 한 많은 관련 정보를 공유하세요. 무엇이, 어디에서, 언제 발생했는지, 재현 단계, OS 및 AO 버전, 문제가 항상 발생하는지 간헐적으로 발생하는지를 포함하세요. 이런 정보는 에이전트가 버그를 재현하고 담당자가 바로 조치할 수 있는 보고서를 제출할 가능성을 높입니다.

```text
Read and follow https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Please reproduce and triage this bug, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

[Discord의 bug-triaging 채널](https://discord.com/channels/1476302178913357958/1491735678156013588)에서도 버그를 신고할 수 있습니다. `@AO Bot#8425`를 태그하고, 어떤 일이 발생했는지 설명한 뒤 bug-triage skill을 사용하도록 요청하세요.

```text
@AO Bot#8425 Please reproduce and triage this bug using the bug-triage skill, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

## 개발 및 기여

코드, 문서, 트리아지, 예제, 테스트 등 다양한 기여를 환영합니다.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

사전 요구 사항, 로컬 설정, 테스트 명령은 [개발 가이드](../docs/development.md)부터 확인하세요. 풀 리퀘스트를 열기 전에 [CONTRIBUTING.md](../CONTRIBUTING.md)를 읽고, 버그와 기능 요청에는 [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues)를 사용하세요.

## 문서

| 문서                                                                | 다음 정보가 필요할 때                                               |
| ------------------------------------------------------------------- | ------------------------------------------------------------------- |
| [제품 문서](https://aoagents.dev/docs)                              | 설치, 에이전트 설정, 일상적인 제품 사용법.                          |
| [docs/architecture.md](../docs/architecture.md)                     | 백엔드 멘탈 모델, 라이프사이클, 영속성, CDC, 상태 도출, 데몬 경계.  |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | 패키지 소유권과 각 백엔드 관심사가 속하는 위치.                     |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI 동작과 데몬 라우트 매핑.                                        |
| [docs/development.md](../docs/development.md)                       | 로컬 개발을 위한 사전 요구 사항, 빌드 단계, 테스트 실행, 문제 해결. |
| [docs/STATUS.md](../docs/STATUS.md)                                 | 현재 `main`에 출시된 내용과 아직 진행 중인 항목.                    |

## AO의 여정 팔로우하기

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="X에 게시된 Agent Orchestrator 개발 업데이트" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="X에 게시된 Agent Orchestrator 개발 업데이트" />
      </a>
    </td>
  </tr>
</table>

## 커뮤니티

도움 및 기여자 논의를 위해 [Discord](https://discord.com/invite/UZv7JjxbwG)에 참여하고, 업데이트를 보려면 [@aoagents](https://x.com/aoagents)를 팔로우하세요. [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues)에서 대화를 시작할 수도 있습니다.

## 익명 텔레메트리

AO는 PII와 프로젝트 콘텐츠를 제외하도록 설계된 개인정보 보호 중심의 제품 사용 및 안정성 지표를 사용합니다. 이 지표는 도입 현황을 파악하고 제품을 개선하는 데 도움이 됩니다. [텔레메트리와 개인정보 보호에 대해 자세히 알아보세요](../docs/telemetry.md).

## 라이선스

Agent Orchestrator는 [Apache License 2.0](../LICENSE)에 따라 제공됩니다.
