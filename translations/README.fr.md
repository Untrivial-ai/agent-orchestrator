<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### Planifiez, exécutez et supervisez vos agents de développement depuis un seul endroit.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Donnez à chaque tâche de développement son propre agent, son propre espace de travail et sa propre boucle de feedback.<br />
Planifiez et déléguez les objectifs plus vastes avec un orchestrateur qui connaît votre projet.<br />
Suivez chaque worker, pull request, exécution CI et revue sur un Kanban actualisé en direct.

[**Télécharger AO**](#installation) &nbsp;&bull;&nbsp; [Documentation](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [Versions](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Contribuer](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · **Français** · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Kanban d'Agent Orchestrator montrant les sessions de workers regroupées selon leur état en direct" width="100%" />
</div>

## Un espace de travail pour le développement piloté par des agents

Un agent de développement peut prendre en charge une tâche. Dès que plusieurs agents travaillent en parallèle sur un projet, le défi change : il faut décider de ce qui compte, découper le travail proprement, donner à chaque agent le bon contexte, éviter les collisions entre branches et suivre chaque changement jusqu'à la revue et à la fusion.

AO est un espace de travail local conçu pour ce rôle. Ajoutez un dépôt, puis créez une session de worker avec l'agent, le modèle et l'interface adaptés à la tâche. Pour les travaux suivis avec Git, AO attribue au worker sa propre branche et son propre worktree. La tâche, la conversation, le terminal, les fichiers modifiés, l'aperçu du navigateur, la pull request, la CI et l'état des revues restent attachés à cette session du début à la fin.

Derrière l'application de bureau, le daemon local d'AO observe l'activité des agents et l'état du contrôle de version. Vous obtenez ainsi une vue partagée et actualisée du projet, au lieu d'une collection de terminaux, de branches et d'onglets de navigateur déconnectés.

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## Les workers exécutent des tâches bien délimitées

Un worker est l'unité d'exécution d'AO : une tâche, un agent de développement et un espace de travail isolé. Utilisez **New task** lorsque le travail est déjà clair. Décrivez le résultat attendu, choisissez un agent et un modèle, joignez les fichiers utiles, puis travaillez avec l'agent dans le Chat structuré ou dans son interface de terminal native.

Vous pouvez ouvrir un worker à tout moment pour poursuivre la conversation, vous connecter à son terminal, examiner ses changements, utiliser son navigateur isolé, relire sa pull request ou renvoyer les retours de CI et de revue au même agent. Chaque tâche reste ainsi compréhensible indépendamment, sans que le travail parallèle ne se mélange dans un contexte partagé.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## L'orchestrateur planifie à l'échelle du projet

L'orchestrateur du projet est l'agent permanent de planification et de coordination d'AO. Il travaille à un niveau supérieur à celui des tâches individuelles : la direction produit, la stratégie technique, les priorités et l'ordre des travaux dans l'ensemble du dépôt.

Utilisez l'orchestrateur pour explorer une idée avant son implémentation, élaborer des approches produit et techniques, raisonner sur les compromis, repérer les tâches à fort impact et transformer un objectif ambigu en plan concret. Sa conversation, liée au projet, conserve les objectifs, les décisions, les contraintes et les raisonnements précédents. Il associe cet historique de planification au contexte du dépôt et à l'état actuel d'AO, notamment les workers actifs, les responsabilités, les pull requests, la CI et les revues. La planification reste ainsi ancrée dans le projet et dans le travail déjà en cours.

Lorsqu'un plan devient exploitable, l'orchestrateur peut le découper en tâches ciblées, lancer ou réorienter des workers, transmettre à chacun le contexte pertinent, suivre leur progression et coordonner la suite. L'orchestrateur prend en charge la planification et la délégation. Les workers prennent en charge l'implémentation, les tests, les commits et les pull requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## Le Kanban rend le système lisible

Chaque worker apparaît sur le même tableau en direct, qu'il ait été lancé depuis **New task** ou délégué par l'orchestrateur. AO détermine la position de chaque carte à partir des faits concernant la session, la pull request, la CI et la revue. Le Kanban devient ainsi une vue opérationnelle du projet :

- **Working:** workers en cours d'implémentation ou prêts à recevoir une nouvelle instruction
- **Needs you:** sessions bloquées, informations manquantes, CI en échec, changements demandés ou signaux perdus
- **In review:** pull requests ouvertes ou en brouillon qui attendent des vérifications ou une revue
- **Ready to merge:** travaux approuvés ou prêts à fusionner ; les sessions fusionnées restent visibles jusqu'à leur archivage

Chaque carte rassemble la tâche, l'agent, la branche, l'activité, la pull request et l'état. Ouvrez-la pour examiner la conversation ou le terminal, les fichiers modifiés, le résumé de la PR, les revues et l'aperçu. Le tableau indique ce qui avance, ce qui est bloqué et où votre attention aura le plus d'impact.

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## Un seul workflow, de l'idée à la fusion

1. **Commencez au bon niveau.** Confiez une tâche claire directement à un worker, ou développez un objectif plus large avec l'orchestrateur du projet et laissez-le structurer le plan.
2. **Déléguez un travail ciblé.** Lancez vous-même les workers, ou demandez à l'orchestrateur de les créer avec le contexte et les responsabilités nécessaires.
3. **Travaillez en isolation.** Chaque worker suivi avec Git reçoit sa propre branche et son propre worktree. Les workers Scratch reçoivent des répertoires sans branche gérés par AO.
4. **Supervisez l'état en direct.** AO suit l'activité des agents, les pull requests, la CI, les retours de revue et les conflits de fusion, puis reflète ces faits dans le Kanban.
5. **Fermez la boucle de feedback.** Examinez directement chaque worker, prenez les décisions à l'échelle du projet avec l'orchestrateur et renvoyez les échecs exploitables ou les commentaires de revue à l'agent responsable.

AO fonctionne avec les agents de développement et le workflow de contrôle de version que vous utilisez déjà. Les agents conservent leurs points forts. AO apporte le contexte du projet, l'exécution isolée, la coordination et la vue opérationnelle qui leur permettent de fonctionner comme un système.

## Points forts du produit

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests et revues par des agents</h3>
      <p>Gardez la CI, la fusionnabilité, l'état des relecteurs et les revues interactives par des agents aux côtés du worker, puis renvoyez les changements demandés au même responsable.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Session de worker avec pull request, CI et état de revue par des agents dans Agent Orchestrator" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Navigateur contrôlable par les agents</h3>
      <p>Prévisualisez et inspectez l'application locale d'un worker à côté de son interface. Les profils de navigateur sont isolés par worker afin que les tâches d'interface parallèles ne partagent pas leur état.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Un worker contrôle son aperçu isolé dans le navigateur intégré" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Interfaces natives, un seul superviseur</h3>
      <p>Utilisez le Chat structuré ou l'interface de terminal native de l'agent, tandis qu'AO conserve le contexte, l'état de l'espace de travail et les retours au même endroit.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## Agents pris en charge

**26 agents de développement pris en charge** dans un workflow supervisé unique.

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

[Consulter les guides de configuration des agents →](https://aoagents.dev/docs/plugins/agents)

**Utilisez l'interface adaptée au moment : le Chat structuré ou l'interface de terminal native de l'agent.**

## Installation

Téléchargez la dernière application de bureau AO pour votre plateforme. AO vérifie automatiquement la disponibilité des mises à jour.

| Plateforme            | Téléchargement                                                                                                                   |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Télécharger](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Ouvrez Agent Orchestrator et sélectionnez le dépôt qu'AO doit gérer. L'application de bureau exécute le daemon pour vous, aucune CLI n'est donc nécessaire. Consultez le [guide d'installation](https://aoagents.dev/docs/installation) pour configurer les CLI des agents et résoudre les problèmes.

## Signaler un bug

La méthode recommandée pour signaler un bug consiste à demander à votre agent de développement de suivre la [skill de triage des bugs](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) du dépôt. Elle guide l'agent pour reproduire le problème sur le code actuel, recueillir les diagnostics, remonter le chemin de code concerné, rechercher les doublons et créer ou mettre à jour une issue GitHub détaillée.

Que vous sollicitiez un agent de développement local ou AO Bot sur Discord, joignez des captures d'écran et partagez autant d'informations pertinentes que possible. Indiquez ce qui s'est passé, où et quand, les étapes de reproduction, votre système d'exploitation, votre version d'AO et si le problème est systématique ou intermittent. L'agent aura ainsi les meilleures chances de reproduire le bug et de rédiger un rapport exploitable.

```text
Lis et suis https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Reproduis ce bug et effectue son triage, puis crée ou mets à jour l'issue GitHub. Contexte : <ce qui s'est passé, où, quand, étapes de reproduction, système d'exploitation, version d'AO et fréquence>. Captures d'écran : <joindre les captures disponibles>.
```

Vous pouvez également signaler un bug dans le [canal de triage des bugs sur Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Mentionnez `@AO Bot#8425`, décrivez le problème et demandez-lui d'utiliser la skill de triage des bugs.

```text
@AO Bot#8425 Reproduis ce bug et effectue son triage avec la skill de triage des bugs, puis crée ou mets à jour l'issue GitHub. Contexte : <ce qui s'est passé, où, quand, étapes de reproduction, système d'exploitation, version d'AO et fréquence>. Captures d'écran : <joindre les captures disponibles>.
```

## Développer et contribuer

Les contributions au code, à la documentation, au triage, aux exemples et aux tests sont les bienvenues.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

Commencez par le [guide de développement](../docs/development.md) pour connaître les prérequis, la configuration locale et les commandes de test. Lisez [CONTRIBUTING.md](../CONTRIBUTING.md) avant d'ouvrir une pull request et utilisez les [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) pour les bugs et les demandes de fonctionnalités.

## Documentation

| Document                                                            | Commencez ici pour                                                                                   |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [Documentation produit](https://aoagents.dev/docs)                  | Installation, configuration des agents et utilisation quotidienne du produit.                        |
| [docs/architecture.md](../docs/architecture.md)                     | Modèle mental du backend, cycle de vie, persistance, CDC, dérivation de l'état et limites du daemon. |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Responsabilité des paquets et emplacement de chaque domaine du backend.                              |
| [docs/cli/README.md](../docs/cli/README.md)                         | Comportement de la CLI et correspondance avec les routes du daemon.                                  |
| [docs/development.md](../docs/development.md)                       | Prérequis, compilation, tests et dépannage pour le développement local.                              |
| [docs/STATUS.md](../docs/STATUS.md)                                 | Ce qui est actuellement livré sur `main` et ce qui reste en cours.                                   |

## Suivez notre parcours

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="Actualité du parcours d'Agent Orchestrator sur X" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="Actualité du parcours d'Agent Orchestrator sur X" />
      </a>
    </td>
  </tr>
</table>

## Communauté

Rejoignez [Discord](https://discord.com/invite/UZv7JjxbwG) pour obtenir de l'aide et échanger avec les contributeurs, suivez [@aoagents](https://x.com/aoagents) pour les nouveautés ou lancez une discussion dans les [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues).

## Télémétrie anonyme

AO utilise des métriques d'usage et de fiabilité respectueuses de la vie privée, conçues pour exclure les données personnelles et le contenu des projets. Ces métriques nous aident à comprendre l'adoption et à améliorer le produit. [En savoir plus sur la télémétrie et la confidentialité](../docs/telemetry.md).

## Licence

Agent Orchestrator est disponible sous [licence Apache 2.0](../LICENSE).
