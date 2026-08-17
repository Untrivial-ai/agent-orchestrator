<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### Planeje, execute e supervisione agentes de programação em um só lugar.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Entre os 6 mil maiores repositórios](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![Versão no GitHub](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![Downloads no GitHub](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![Licença: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Dê a cada tarefa de programação seu próprio agente, workspace e ciclo de feedback.<br />
Planeje e delegue objetivos maiores com um orquestrador que conhece o projeto.<br />
Acompanhe cada worker, pull request, execução de CI e revisão em um Kanban ao vivo.

[**Baixar o AO**](#instalação) &nbsp;&bull;&nbsp; [Documentação](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [Versões](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Como contribuir](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · **Português (Brasil)**

<br />

<img src="../docs/assets/readme/hero.png" alt="Kanban do Agent Orchestrator com sessões de workers agrupadas por status em tempo real" width="100%" />
</div>

## Um workspace para desenvolvimento orientado por agentes

Um agente de programação pode cuidar de uma tarefa. Quando vários agentes trabalham em paralelo no mesmo projeto, surge um trabalho diferente: decidir o que importa, dividir o trabalho com clareza, dar o contexto certo a cada agente, evitar conflitos entre branches e acompanhar cada mudança até a revisão e o merge.

O AO é um workspace desktop local criado para esse trabalho. Adicione um repositório e crie uma sessão de worker com o agente de programação, o modelo e a interface adequados para a tarefa. Em trabalhos com Git, o AO dá ao worker sua própria branch e seu próprio worktree. Tarefa, conversa, terminal, arquivos alterados, prévia no navegador, pull request, CI e estado de revisão permanecem associados à sessão do início ao fim.

Por trás do aplicativo desktop, o daemon local do AO monitora a atividade dos agentes e o estado do controle de versão. O resultado é uma visão compartilhada e atualizada do projeto, em vez de um conjunto de terminais, branches e abas do navegador desconectados.

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## Workers executam tarefas focadas

Um worker é a unidade de execução do AO: uma tarefa, um agente de programação e um workspace isolado. Use **New task** quando o trabalho já estiver claro. Descreva o resultado desejado, escolha um agente e um modelo, anexe os arquivos relevantes e trabalhe com o agente pelo Chat estruturado ou pela interface nativa de terminal.

Abra um worker a qualquer momento para continuar a conversa, conectar-se ao terminal, inspecionar as mudanças, usar o navegador isolado, revisar o pull request ou devolver ao mesmo agente o feedback de CI e de revisão. Assim, cada tarefa permanece compreensível por conta própria, e o trabalho paralelo não se mistura em um único contexto compartilhado.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## O orquestrador planeja o projeto como um todo

O orquestrador do projeto é o agente persistente de planejamento e coordenação do AO. Ele atua em um nível acima das tarefas individuais: direção do produto, estratégia técnica, prioridades e sequência de trabalho em todo o repositório.

Use o orquestrador para explorar uma ideia antes da implementação, discutir abordagens de produto e técnicas, analisar trade-offs, identificar trabalho de alto impacto e transformar um objetivo ambíguo em um plano concreto. A conversa vinculada ao projeto preserva objetivos, decisões, restrições e raciocínios anteriores. O orquestrador combina esse histórico de planejamento com o contexto do repositório e o estado atual do AO, incluindo workers ativos, responsáveis, pull requests, CI e revisões. Assim, o planejamento permanece conectado tanto ao projeto quanto ao trabalho que já está em andamento.

Quando um plano está pronto para sair do papel, o orquestrador pode dividi-lo em tarefas focadas, iniciar ou redirecionar workers, passar o contexto relevante a cada um, acompanhar o progresso e coordenar o trabalho seguinte. O orquestrador cuida do planejamento e da delegação; os workers cuidam da implementação, dos testes, dos commits e dos pull requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## O Kanban torna o sistema compreensível

Todo worker aparece no mesmo quadro ao vivo, independentemente de ter sido iniciado por **New task** ou delegado pelo orquestrador. O AO determina a posição de cada cartão a partir dos fatos da sessão, do pull request, da CI e da revisão, transformando o Kanban em uma visão operacional do projeto:

- **Working (Em andamento):** workers implementando ativamente ou prontos para receber outra instrução
- **Needs you (Precisa de você):** sessões bloqueadas, informações ausentes, falhas de CI, mudanças solicitadas ou perda de sinal
- **In review (Em revisão):** pull requests abertos ou em rascunho aguardando verificações ou revisão
- **Ready to merge (Pronto para merge):** trabalho aprovado ou pronto para merge; sessões já integradas continuam visíveis até serem arquivadas

Cada cartão reúne tarefa, agente, branch, atividade, pull request e status. Abra-o para inspecionar a conversa ou o terminal, os arquivos alterados, o resumo do PR, as revisões e a prévia. O quadro mostra o que está avançando, o que está bloqueado e onde sua atenção terá mais impacto.

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## Um fluxo único, da ideia ao merge

1. **Comece no nível certo.** Entregue uma tarefa clara diretamente a um worker ou desenvolva um objetivo maior com o orquestrador do projeto e deixe que ele estruture o plano.
2. **Delegue trabalho focado.** Inicie workers por conta própria ou peça ao orquestrador que os crie com o contexto e a responsabilidade necessários.
3. **Construa com isolamento.** Cada worker associado ao Git recebe sua própria branch e seu próprio worktree; workers Scratch recebem diretórios sem branch gerenciados pelo AO.
4. **Supervisione o estado em tempo real.** O AO acompanha a atividade dos agentes, pull requests, CI, feedback de revisão e conflitos de merge, e reflete esses fatos no Kanban.
5. **Feche o ciclo de feedback.** Inspecione qualquer worker diretamente, tome decisões sobre o projeto com o orquestrador e devolva falhas acionáveis ou comentários de revisão ao agente responsável pelo trabalho.

O AO funciona com os agentes de programação e o fluxo de controle de versão que você já usa. Os agentes mantêm seus pontos fortes nativos; o AO fornece o contexto do projeto, a execução isolada, a coordenação e a visão operacional que fazem esses agentes trabalhar como um sistema.

## Destaques do produto

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests e revisões por agentes</h3>
      <p>Mantenha a CI, a possibilidade de merge, o estado dos revisores e as revisões interativas por agentes junto ao worker, e depois devolva as mudanças solicitadas ao mesmo responsável.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Sessão de worker com pull request, CI e estado de revisão por agente no Agent Orchestrator" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Navegador controlável pelo agente</h3>
      <p>Visualize e inspecione o aplicativo local de um worker ao lado de sua interface. Os perfis de navegador são isolados por worker, para que tarefas paralelas de interface não compartilhem estado.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Um worker controlando sua prévia isolada no navegador integrado" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Interfaces nativas, um único supervisor</h3>
      <p>Use o Chat estruturado ou a interface de terminal nativa do agente enquanto o AO mantém o contexto, o estado do workspace e o feedback em um só lugar.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## Agentes compatíveis

**26 agentes de programação compatíveis** em um único fluxo supervisionado.

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

[Consulte os guias de configuração dos agentes →](https://aoagents.dev/docs/plugins/agents)

**Use a interface adequada para cada momento: o Chat estruturado ou a interface nativa de terminal do agente.**

## Instalação

Baixe a versão mais recente do aplicativo desktop do AO para sua plataforma. O AO verifica atualizações automaticamente.

| Plataforma            | Download                                                                                                                      |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Abra o Agent Orchestrator e indique o repositório que você deseja que o AO gerencie. O aplicativo desktop executa o daemon para você, portanto não é necessário usar a CLI. Consulte o [guia de instalação](https://aoagents.dev/docs/installation) para configurar as CLIs dos agentes e resolver problemas.

## Relatar um bug

A forma recomendada de relatar um bug é pedir ao seu agente de programação que siga a [skill de triagem de bugs](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) do repositório. Ela orienta o agente a reproduzir o problema no código atual, coletar diagnósticos, rastrear o caminho relevante no código, verificar duplicatas e abrir ou atualizar uma issue detalhada no GitHub.

Tanto ao pedir ajuda a um agente de programação local quanto ao AO Bot no Discord, anexe capturas de tela e compartilhe o máximo possível de contexto relevante. Inclua o que aconteceu, onde e quando aconteceu, as etapas para reproduzir, seu sistema operacional e a versão do AO, além de informar se o problema acontece sempre ou de forma intermitente. Isso dá ao agente a melhor chance de reproduzir o bug e registrar um relato acionável.

```text
Leia esta skill e siga as instruções:
https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md
Reproduza e faça a triagem deste bug e, em seguida, abra ou atualize a issue no GitHub. Contexto: <o que aconteceu, onde, quando, etapas para reproduzir, sistema operacional, versão do AO e frequência>. Capturas de tela: <anexe as capturas disponíveis>.
```

Você também pode relatar um bug no [canal bug-triaging do Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Marque `@AO Bot#8425`, descreva o que aconteceu e peça que ele use a skill de triagem de bugs.

```text
@AO Bot#8425 Reproduza e faça a triagem deste bug usando a skill de triagem de bugs e, em seguida, abra ou atualize a issue no GitHub. Contexto: <o que aconteceu, onde, quando, etapas para reproduzir, sistema operacional, versão do AO e frequência>. Capturas de tela: <anexe as capturas disponíveis>.
```

## Desenvolver e contribuir

Contribuições em código, documentação, triagem, exemplos e testes são bem-vindas.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

Comece pelo [guia de desenvolvimento](../docs/development.md) para ver pré-requisitos, configuração local e comandos de teste. Leia [CONTRIBUTING.md](../CONTRIBUTING.md) antes de abrir um pull request e use as [Issues do GitHub](https://github.com/Untrivial-ai/agent-orchestrator/issues) para bugs e solicitações de recursos.

## Documentação

| Documento                                                           | Comece aqui quando precisar de                                                                       |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [Documentação do produto](https://aoagents.dev/docs)                | Instalação, configuração de agentes e uso cotidiano do produto.                                      |
| [docs/architecture.md](../docs/architecture.md)                     | Modelo mental do backend, ciclo de vida, persistência, CDC, derivação de status e limites do daemon. |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Responsabilidade dos pacotes e onde cada aspecto do backend deve ficar.                              |
| [docs/cli/README.md](../docs/cli/README.md)                         | Comportamento da CLI e mapeamento das rotas do daemon.                                               |
| [docs/development.md](../docs/development.md)                       | Pré-requisitos, etapas de build, execução de testes e solução de problemas no desenvolvimento local. |
| [docs/STATUS.md](../docs/STATUS.md)                                 | O que está disponível atualmente em `main` e o que ainda está em desenvolvimento.                    |

## Acompanhe a jornada

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="Atualização sobre a jornada do Agent Orchestrator no X" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="Atualização sobre a jornada do Agent Orchestrator no X" />
      </a>
    </td>
  </tr>
</table>

## Comunidade

Participe do [Discord](https://discord.com/invite/UZv7JjxbwG) para receber ajuda e conversar com outros colaboradores, siga [@aoagents](https://x.com/aoagents) para acompanhar as novidades ou inicie uma conversa nas [Issues do GitHub](https://github.com/Untrivial-ai/agent-orchestrator/issues).

## Telemetria anônima

O AO usa métricas de uso do produto e confiabilidade que preservam a privacidade e foram projetadas para excluir informações de identificação pessoal e conteúdo dos projetos. Essas métricas nos ajudam a entender a adoção e melhorar o produto. [Saiba mais sobre telemetria e privacidade](../docs/telemetry.md).

## Licença

O Agent Orchestrator está disponível sob a [Licença Apache 2.0](../LICENSE).
