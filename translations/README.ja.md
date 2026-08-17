<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### コーディングエージェントを使った作業の計画、実行、監督をひとつの場所で。

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

すべてのコーディングタスクに、それぞれ専用のエージェント、ワークスペース、フィードバックループを。<br />
プロジェクトを理解するオーケストレーターとともに、より大きな成果を計画し委任できます。<br />
すべてのワーカー、プルリクエスト、CI、レビューをライブ Kanban で追跡できます。

[**AO をダウンロード**](#インストール) &nbsp;&bull;&nbsp; [ドキュメント](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [リリース](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [コントリビューション](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · **日本語** · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="ワーカーセッションを現在の状態ごとに表示する Agent Orchestrator の Kanban" width="100%" />
</div>

## エージェント駆動開発のためのワークスペース

1 つのコーディングエージェントなら、1 つのタスクを処理できます。しかし、プロジェクト全体で複数のエージェントを動かすには、別の種類の仕事が生まれます。何が重要かを判断し、作業を適切に分割し、各エージェントに必要なコンテキストを渡し、ブランチの衝突を防ぎ、すべての変更をレビューからマージまで見届ける必要があります。

AO は、その仕事のために作られたローカルのデスクトップワークスペースです。リポジトリを追加し、タスクに合ったコーディングエージェント、モデル、インターフェースを選んでワーカーセッションを作成します。Git を使う作業では、AO がワーカー専用のブランチと worktree を用意します。タスク、会話、ターミナル、変更ファイル、ブラウザプレビュー、プルリクエスト、CI、レビューの状態は、最初から最後までそのセッションに紐づいたままです。

デスクトップアプリの背後では、AO のローカルデーモンがエージェントの活動とソース管理の状態を監視します。その結果、分断されたターミナル、ブランチ、ブラウザタブの集まりではなく、プロジェクト全体をひとつの共有ライブビューで把握できます。

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## ワーカーが明確なタスクを実行する

ワーカーは AO における実行の単位です。1 つのタスク、1 つのコーディングエージェント、1 つの隔離されたワークスペースで構成されます。やるべきことが明確な場合は、**New task** を使います。求める成果を説明し、エージェントとモデルを選び、関連ファイルを添付して、構造化された Chat またはエージェント本来のターミナル UI で作業します。

ワーカーはいつでも開き直せます。会話を続ける、ターミナルに接続する、変更内容を確認する、隔離されたブラウザを使う、プルリクエストをレビューする、CI やレビューのフィードバックを同じエージェントへ戻す、といった操作が可能です。これにより、各タスクを単独で理解できる状態に保ち、並行作業がひとつの共有コンテキストへ崩れてしまうことを防ぎます。

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## オーケストレーターがプロジェクト全体を計画する

プロジェクトオーケストレーターは、AO の永続的な計画・調整エージェントです。個別のタスクより一段上の視点で、プロダクトの方向性、技術戦略、優先順位、リポジトリ全体にわたる作業の順序を扱います。

実装に入る前のアイデア探索、プロダクトや技術アプローチのブレインストーミング、トレードオフの検討、インパクトの大きい作業の特定、曖昧なゴールから具体的な計画への落とし込みにオーケストレーターを使えます。プロジェクト単位の会話には、目標、意思決定、制約、過去の検討内容が蓄積されます。さらに、その計画履歴をリポジトリのコンテキストや AO のライブ状態と組み合わせます。進行中のワーカー、担当範囲、プルリクエスト、CI、レビューまで把握するため、計画はプロジェクトと現在進んでいる作業の両方に根ざしたものになります。

計画が実行可能になれば、オーケストレーターはそれを明確なタスクに分割し、ワーカーを起動または誘導し、それぞれに必要なコンテキストを渡し、進捗を追い、後続作業を調整できます。オーケストレーターは計画と委任を担い、ワーカーは実装、テスト、コミット、プルリクエストを担います。

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## Kanban がシステム全体を見通せる状態に保つ

**New task** から直接始めた場合も、オーケストレーターが委任した場合も、すべてのワーカーは同じライブボードに表示されます。AO はセッション、プルリクエスト、CI、レビューの事実から各カードの位置を導き出し、Kanban をプロジェクト運用のためのビューにします。

- **Working:** 実装中、または次の指示を受けられるワーカー
- **Needs you:** ブロック中、入力待ち、CI 失敗、変更要求、シグナル消失のいずれかに該当するセッション
- **In review:** チェックまたはレビューを待っているオープン中またはドラフトのプルリクエスト
- **Ready to merge:** 承認済み、またはマージ可能な作業。マージ済みのセッションもアーカイブされるまで表示されます

各カードには、タスク、エージェント、ブランチ、活動状況、プルリクエスト、ステータスがまとまっています。カードを開けば、会話またはターミナル、変更ファイル、PR の概要、レビュー、プレビューを確認できます。ボードを見るだけで、何が進んでいるか、何が止まっているか、どこに注意を向けると最も効果的かが分かります。

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## アイデアからマージまで、ひとつのワークフローで

1. **適切なレベルから始める。** 明確なタスクはワーカーに直接渡します。より大きな成果はプロジェクトオーケストレーターと検討し、計画を組み立てます。
2. **作業を明確に分けて委任する。** 自分でワーカーを起動するか、オーケストレーターに必要なコンテキストと担当範囲を持つワーカーを作成させます。
3. **隔離された環境で開発する。** Git を使うすべてのワーカーには専用のブランチと worktree が与えられます。Scratch ワーカーには AO が管理するブランチなしのディレクトリが与えられます。
4. **ライブ状態を監督する。** AO はエージェントの活動、プルリクエスト、CI、レビューのフィードバック、マージコンフリクトを追跡し、それらの事実を Kanban に反映します。
5. **フィードバックループを閉じる。** 任意のワーカーを直接確認し、オーケストレーターとプロジェクト全体の判断を行い、対処可能な失敗やレビューコメントを、その作業を担当するエージェントへ戻します。

AO は、すでに利用しているコーディングエージェントやソース管理のワークフローと連携します。エージェントはそれぞれ本来の強みを維持し、AO はそれらをひとつのシステムとして機能させるためのプロジェクトコンテキスト、隔離された実行環境、調整、運用ビューを提供します。

## プロダクトの特長

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>プルリクエストとエージェントレビュー</h3>
      <p>CI、マージ可能性、レビュアーの状態、対話型のエージェントレビューをワーカーのそばに集約し、要求された変更を同じ担当エージェントへ戻せます。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Agent Orchestrator でプルリクエスト、CI、エージェントレビューの状態を表示しているワーカーセッション" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>エージェントが操作できるブラウザ</h3>
      <p>ワーカーのインターフェースの隣でローカルアプリをプレビューして調査できます。ブラウザプロファイルはワーカーごとに隔離されるため、並行する UI タスクで状態が共有されることはありません。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="隔離されたアプリ内ブラウザプレビューを操作するワーカー" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>ネイティブなインターフェースを一つの監督画面で</h3>
      <p>構造化された Chat またはエージェント本来のターミナル UI を使いながら、AO がタスクのコンテキスト、ワークスペースの状態、フィードバックを一か所にまとめます。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## 対応エージェント

**26 種類のコーディングエージェント**をひとつの監督されたワークフローで利用できます。

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

[エージェントのセットアップガイドを見る →](https://aoagents.dev/docs/plugins/agents)

**状況に合ったインターフェースを選べます。構造化された Chat と、エージェント本来のターミナル UI に対応しています。**

## インストール

お使いのプラットフォーム向けの最新 AO デスクトップアプリをダウンロードしてください。AO は更新を自動的に確認します。

| プラットフォーム       | ダウンロード                                                                                                                      |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| macOS（Apple silicon） | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS（Intel）         | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows                | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux（AppImage）      | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux（Debian/Ubuntu） | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux（Fedora/RHEL）   | [ダウンロード](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Agent Orchestrator を開き、AO に管理させたいリポジトリを指定します。デスクトップアプリがデーモンを実行するため、CLI は不要です。エージェント CLI のセットアップとトラブルシューティングは[インストールガイド](https://aoagents.dev/docs/installation)を参照してください。

## バグを報告する

推奨するバグ報告方法は、コーディングエージェントに、このリポジトリの [bug-triage skill](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) に従うよう依頼することです。この skill は、現在のコードで問題を再現し、診断情報を収集し、関連するコードパスを追跡し、重複を確認したうえで、詳細な GitHub Issue を新規作成または更新するようエージェントを導きます。

ローカルのコーディングエージェントに依頼する場合も、Discord の AO Bot に依頼する場合も、スクリーンショットを添付し、できるだけ多くの関連情報を共有してください。何が、どこで、いつ起きたのか、再現手順、OS と AO のバージョン、常に発生するのか断続的なのかを含めます。こうした情報があれば、エージェントが問題を再現し、担当者が対応しやすい報告を作成できる可能性が高まります。

```text
Read and follow https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Please reproduce and triage this bug, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

[Discord の bug-triaging チャンネル](https://discord.com/channels/1476302178913357958/1491735678156013588)から報告することもできます。`@AO Bot#8425` をタグ付けし、何が起きたかを説明して、bug-triage skill を使うよう依頼してください。

```text
@AO Bot#8425 Please reproduce and triage this bug using the bug-triage skill, then file or update the GitHub issue. Context: <what happened, where, when, reproduction steps, OS, AO version, and frequency>. Screenshots: <attach any screenshots>.
```

## 開発とコントリビューション

コード、ドキュメント、トリアージ、サンプル、テストなど、さまざまな形でのコントリビューションを歓迎します。

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

前提条件、ローカル環境のセットアップ、テストコマンドについては、まず[開発ガイド](../docs/development.md)を確認してください。プルリクエストを作成する前に [CONTRIBUTING.md](../CONTRIBUTING.md) を読み、バグや機能リクエストには [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) を利用してください。

## ドキュメント

| ドキュメント                                                        | 次の情報が必要な場合                                                                      |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| [プロダクトドキュメント](https://aoagents.dev/docs)                 | インストール、エージェントのセットアップ、日常的な製品の使い方。                          |
| [docs/architecture.md](../docs/architecture.md)                     | バックエンドのメンタルモデル、ライフサイクル、永続化、CDC、ステータス導出、デーモン境界。 |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | パッケージの所有範囲と、バックエンドの各関心事を配置する場所。                            |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI の動作とデーモンルートの対応関係。                                                    |
| [docs/development.md](../docs/development.md)                       | ローカル開発の前提条件、ビルド手順、テスト実行、トラブルシューティング。                  |
| [docs/STATUS.md](../docs/STATUS.md)                                 | `main` で現在提供されているものと、まだ進行中のもの。                                     |

## AO の歩みをフォローする

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="X に投稿された Agent Orchestrator の開発アップデート" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="X に投稿された Agent Orchestrator の開発アップデート" />
      </a>
    </td>
  </tr>
</table>

## コミュニティ

サポートやコントリビューター同士の議論には [Discord](https://discord.com/invite/UZv7JjxbwG) に参加し、最新情報は [@aoagents](https://x.com/aoagents) をフォローしてください。[GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) から会話を始めることもできます。

## 匿名テレメトリ

AO は、PII とプロジェクトの内容を除外するよう設計された、プライバシーに配慮した製品利用状況と信頼性の指標を使用します。これらの指標は、導入状況の把握と製品改善に役立てられます。[テレメトリとプライバシーについて詳しく見る](../docs/telemetry.md)。

## ライセンス

Agent Orchestrator は [Apache License 2.0](../LICENSE) のもとで提供されています。
