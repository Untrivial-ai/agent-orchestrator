<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### Coding-Agenten an einem Ort planen, ausführen und überwachen.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Gib jeder Coding-Aufgabe einen eigenen Agenten, Workspace und Feedback-Zyklus.<br />
Plane und delegiere größere Vorhaben mit einem projektkundigen Orchestrator.<br />
Verfolge jeden Worker, Pull Request, CI-Lauf und jedes Review in einem Live-Kanban.

[**AO herunterladen**](#installation) &nbsp;&bull;&nbsp; [Dokumentation](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [Releases](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Mitwirken](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · **Deutsch** · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Agent-Orchestrator-Kanban mit Worker-Sessions, gruppiert nach ihrem Live-Status" width="100%" />
</div>

## Ein Workspace für agentengestützte Entwicklung

Ein Coding-Agent kann eine Aufgabe erledigen. Sobald mehrere Agenten parallel an einem Projekt arbeiten, entsteht eine andere Herausforderung: entscheiden, was wichtig ist, die Arbeit sauber aufteilen, jedem Agenten den richtigen Kontext geben, Branch-Konflikte vermeiden und jede Änderung bis zu Review und Merge begleiten.

AO ist ein lokaler Desktop-Workspace für genau diese Aufgabe. Füge ein Repository hinzu und erstelle eine Worker-Session mit dem Coding-Agenten, dem Modell und der Oberfläche, die am besten zur Aufgabe passen. Bei Git-basierter Arbeit erhält der Worker einen eigenen Branch und Worktree. Aufgabe, Konversation, Terminal, geänderte Dateien, Browser-Vorschau, Pull Request, CI- und Review-Status bleiben von Anfang bis Ende mit dieser Session verbunden.

Hinter der Desktop-App beobachtet AOs lokaler Daemon die Aktivität der Agenten und den Zustand der Versionsverwaltung. So entsteht eine gemeinsame Live-Ansicht des Projekts statt einer Sammlung unverbundener Terminals, Branches und Browser-Tabs.

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## Worker erledigen klar abgegrenzte Aufgaben

Ein Worker ist AOs Ausführungseinheit: eine Aufgabe, ein Coding-Agent und ein isolierter Workspace. Nutze **New task**, wenn die Aufgabe bereits klar ist. Beschreibe das gewünschte Ergebnis, wähle Agent und Modell, hänge relevante Dateien an und arbeite mit dem Agenten im strukturierten Chat oder in seiner nativen Terminal-Oberfläche.

Du kannst einen Worker jederzeit öffnen, um das Gespräch fortzusetzen, sein Terminal aufzurufen, Änderungen zu prüfen, den isolierten Browser zu verwenden, den Pull Request zu begutachten oder CI- und Review-Feedback an denselben Agenten zurückzugeben. Dadurch bleibt jede Aufgabe für sich verständlich und parallele Arbeit landet nicht in einem gemeinsamen Kontext.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## Der Orchestrator plant über das gesamte Projekt hinweg

Der Projekt-Orchestrator ist AOs dauerhafter Planungs- und Koordinationsagent. Er arbeitet eine Ebene über einzelnen Aufgaben und behält Produktentwicklung, technische Strategie, Prioritäten und die Reihenfolge der Arbeit im gesamten Repository im Blick.

Nutze den Orchestrator, um eine Idee vor der Umsetzung zu erkunden, Produkt- und Technikansätze zu entwickeln, Zielkonflikte abzuwägen, besonders wirkungsvolle Aufgaben zu erkennen und aus einem unklaren Vorhaben einen konkreten Plan zu machen. Seine projektbezogene Konversation bewahrt Ziele, Entscheidungen, Einschränkungen und frühere Überlegungen. Diese Planungshistorie verbindet er mit dem Repository-Kontext und AOs aktuellem Zustand, darunter aktive Worker, Zuständigkeiten, Pull Requests, CI und Reviews. Damit bleibt die Planung sowohl im Projekt als auch in der bereits laufenden Arbeit verankert.

Sobald ein Plan umsetzbar ist, kann der Orchestrator ihn in klar abgegrenzte Aufgaben zerlegen, Worker starten oder neu ausrichten, jedem Worker den relevanten Kontext mitgeben, den Fortschritt verfolgen und Folgearbeiten koordinieren. Der Orchestrator verantwortet Planung und Delegation. Worker verantworten Umsetzung, Tests, Commits und Pull Requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## Das Kanban macht das System übersichtlich

Jeder Worker erscheint auf demselben Live-Board, unabhängig davon, ob du ihn über **New task** gestartet oder über den Orchestrator delegiert hast. AO leitet die Position jeder Karte aus Fakten zu Session, Pull Request, CI und Review ab. So wird das Kanban zur operativen Projektansicht:

- **Working:** Worker, die aktiv umsetzen oder für eine weitere Anweisung bereit sind
- **Needs you:** blockierte Sessions, fehlende Eingaben, fehlgeschlagene CI, angeforderte Änderungen oder verlorene Signale
- **In review:** offene Pull Requests und Entwürfe, die auf Checks oder Reviews warten
- **Ready to merge:** genehmigte oder mergefähige Arbeit; zusammengeführte Sessions bleiben sichtbar, bis sie archiviert werden

Jede Karte hält Aufgabe, Agent, Branch, Aktivität, Pull Request und Status zusammen. Öffne sie, um Konversation oder Terminal, geänderte Dateien, PR-Zusammenfassung, Reviews und Vorschau zu prüfen. Das Board zeigt, was vorankommt, was blockiert ist und wo deine Aufmerksamkeit die größte Wirkung hat.

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## Ein Workflow von der Idee bis zum Merge

1. **Starte auf der richtigen Ebene.** Gib eine klare Aufgabe direkt an einen Worker oder entwickle ein größeres Vorhaben mit dem Projekt-Orchestrator und lass ihn den Plan ausarbeiten.
2. **Delegiere fokussierte Arbeit.** Starte Worker selbst oder lass den Orchestrator sie mit dem nötigen Kontext und klarer Zuständigkeit erstellen.
3. **Arbeite isoliert.** Jeder Git-basierte Worker erhält einen eigenen Branch und Worktree. Scratch-Worker erhalten von AO verwaltete Verzeichnisse ohne Branch.
4. **Überwache den Live-Status.** AO verfolgt Agentenaktivität, Pull Requests, CI, Review-Feedback und Merge-Konflikte und bildet diese Fakten im Kanban ab.
5. **Schließe den Feedback-Zyklus.** Prüfe jeden Worker direkt, triff projektweite Entscheidungen mit dem Orchestrator und gib umsetzbare Fehler oder Review-Kommentare an den zuständigen Agenten zurück.

AO arbeitet mit den Coding-Agenten und dem Versionsverwaltungs-Workflow, die du bereits nutzt. Agenten behalten ihre jeweiligen Stärken. AO liefert Projektkontext, isolierte Ausführung, Koordination und die operative Übersicht, die aus ihnen ein System machen.

## Produkthighlights

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull Requests und Agenten-Reviews</h3>
      <p>Halte CI, Mergefähigkeit, Reviewer-Status und interaktive Agenten-Reviews beim Worker und gib angeforderte Änderungen an denselben Verantwortlichen zurück.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Worker-Session mit Pull Request, CI und Agenten-Review-Status in Agent Orchestrator" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Von Agenten steuerbarer Browser</h3>
      <p>Zeige die lokale App eines Workers neben seiner Oberfläche an und prüfe sie dort. Browser-Profile sind pro Worker isoliert, damit parallele UI-Aufgaben keinen Status teilen.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Ein Worker steuert seine isolierte Browser-Vorschau in der App" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Native Oberflächen, eine Aufsicht</h3>
      <p>Nutze strukturierten Chat oder die native Terminal-Oberfläche des Agenten, während AO Aufgaben, Workspace-Status und Feedback an einem Ort zusammenhält.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## Unterstützte Agenten

**26 Coding-Agenten werden unterstützt**, gemeinsam überwacht in einem einzigen Workflow.

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

[Einrichtungsanleitungen für Agenten ansehen →](https://aoagents.dev/docs/plugins/agents)

**Nutze die Oberfläche, die gerade passt: strukturierten Chat oder die native Terminal-Oberfläche des Agenten.**

## Installation

Lade die neueste AO-Desktop-App für deine Plattform herunter. AO sucht automatisch nach Updates.

| Plattform             | Download                                                                                                                           |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Herunterladen](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Öffne Agent Orchestrator und wähle das Repository aus, das AO verwalten soll. Die Desktop-App führt den Daemon für dich aus, eine CLI ist daher nicht erforderlich. Im [Installationsleitfaden](https://aoagents.dev/docs/installation) findest du Hinweise zur Einrichtung der Agenten-CLI und zur Fehlerbehebung.

## Einen Bug melden

Am besten meldest du einen Bug, indem du deinen Coding-Agenten bittest, dem [Bug-Triage-Skill](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) des Repositorys zu folgen. Er führt den Agenten durch die Reproduktion mit dem aktuellen Code, die Sammlung von Diagnosedaten, die Untersuchung des relevanten Codepfads, die Suche nach Duplikaten und das Erstellen oder Aktualisieren eines detaillierten GitHub-Issues.

Egal, ob du einen lokalen Coding-Agenten oder AO Bot auf Discord fragst: Hänge Screenshots an und teile so viel relevanten Kontext wie möglich. Beschreibe, was wann und wo passiert ist, nenne Schritte zur Reproduktion, dein Betriebssystem, deine AO-Version und ob das Problem immer oder nur gelegentlich auftritt. So hat der Agent die besten Chancen, den Bug zu reproduzieren und einen hilfreichen Bericht zu erstellen.

```text
Lies und befolge https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Bitte reproduziere und triagiere diesen Bug und erstelle oder aktualisiere anschließend das GitHub-Issue. Kontext: <was ist wann und wo passiert, Schritte zur Reproduktion, Betriebssystem, AO-Version und Häufigkeit>. Screenshots: <Screenshots anhängen>.
```

Du kannst einen Bug auch im [Bug-Triaging-Kanal auf Discord](https://discord.com/channels/1476302178913357958/1491735678156013588) melden. Erwähne `@AO Bot#8425`, beschreibe das Problem und bitte ihn, den Bug-Triage-Skill zu verwenden.

```text
@AO Bot#8425 Bitte reproduziere und triagiere diesen Bug mit dem Bug-Triage-Skill und erstelle oder aktualisiere anschließend das GitHub-Issue. Kontext: <was ist wann und wo passiert, Schritte zur Reproduktion, Betriebssystem, AO-Version und Häufigkeit>. Screenshots: <Screenshots anhängen>.
```

## Entwickeln und mitwirken

Beiträge zu Code, Dokumentation, Triage, Beispielen und Tests sind willkommen.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

Der [Entwicklungsleitfaden](../docs/development.md) erklärt Voraussetzungen, lokale Einrichtung und Testbefehle. Lies [CONTRIBUTING.md](../CONTRIBUTING.md), bevor du einen Pull Request öffnest, und nutze [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) für Bugs und Funktionswünsche.

## Dokumentation

| Dokument                                                            | Hier findest du                                                                            |
| ------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| [Produktdokumentation](https://aoagents.dev/docs)                   | Installation, Einrichtung von Agenten und tägliche Produktnutzung.                         |
| [docs/architecture.md](../docs/architecture.md)                     | Backend-Mentalmodell, Lifecycle, Persistenz, CDC, Statusableitung und Daemon-Grenzen.      |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Paketverantwortung und Zuordnung der einzelnen Backend-Bereiche.                           |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI-Verhalten und Zuordnung der Daemon-Routen.                                             |
| [docs/development.md](../docs/development.md)                       | Voraussetzungen, Build-Schritte, Testausführung und Fehlerbehebung für lokale Entwicklung. |
| [docs/STATUS.md](../docs/STATUS.md)                                 | Was aktuell auf `main` ausgeliefert wird und woran noch gearbeitet wird.                   |

## Begleite unsere Entwicklung

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="Agent-Orchestrator-Entwicklungsupdate auf X" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="Agent-Orchestrator-Entwicklungsupdate auf X" />
      </a>
    </td>
  </tr>
</table>

## Community

Komm auf unseren [Discord](https://discord.com/invite/UZv7JjxbwG), wenn du Hilfe suchst oder dich mit anderen Mitwirkenden austauschen möchtest. Folge [@aoagents](https://x.com/aoagents) für Neuigkeiten oder starte eine Diskussion in den [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues).

## Anonyme Telemetrie

AO verwendet datenschutzfreundliche Kennzahlen zur Produktnutzung und Zuverlässigkeit, die keine personenbezogenen Daten oder Projektinhalte enthalten sollen. Diese Kennzahlen helfen uns, die Nutzung zu verstehen und das Produkt zu verbessern. [Mehr über Telemetrie und Datenschutz erfahren](../docs/telemetry.md).

## Lizenz

Agent Orchestrator steht unter der [Apache License 2.0](../LICENSE) zur Verfügung.
