<div align="center">
  <img src="../assets/ao-logo.svg" alt="Agent Orchestrator" width="144" height="144" />

### Agent Orchestrator

#### Planifica, ejecuta y supervisa agentes de programación desde un solo lugar.

[![GitHub stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/Untrivial-ai/agent-orchestrator?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/Untrivial-ai/agent-orchestrator/total?style=flat&logo=github)](https://github.com/Untrivial-ai/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@aoagents-555?style=flat&logo=x&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Asigna a cada tarea de programación su propio agente, espacio de trabajo y ciclo de feedback.<br />
Planifica y delega objetivos más amplios con un orquestador que conoce tu proyecto.<br />
Sigue a cada worker, pull request, ejecución de CI y revisión en un Kanban en vivo.

[**Descargar AO**](#instalación) &nbsp;&bull;&nbsp; [Documentación](https://aoagents.dev/docs) &nbsp;&bull;&nbsp; [Versiones](https://github.com/Untrivial-ai/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Contribuir](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · **Español** · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Kanban de Agent Orchestrator con sesiones de workers agrupadas por estado en vivo" width="100%" />
</div>

## Un espacio de trabajo para el desarrollo con agentes

Un agente de programación puede encargarse de una tarea. Cuando varios trabajan en paralelo dentro de un proyecto, el reto cambia: decidir qué importa, dividir bien el trabajo, dar el contexto adecuado a cada agente, evitar colisiones entre ramas y acompañar cada cambio hasta la revisión y la integración.

AO es un espacio de trabajo local de escritorio creado para ese reto. Añade un repositorio y crea una sesión de worker con el agente, el modelo y la interfaz que mejor se adapten a la tarea. Para trabajo respaldado por Git, AO asigna al worker su propia rama y su propio worktree. La tarea, la conversación, el terminal, los archivos modificados, la vista previa del navegador, el pull request, la CI y el estado de revisión permanecen vinculados a esa sesión de principio a fin.

Detrás de la aplicación de escritorio, el daemon local de AO observa la actividad de los agentes y el estado del control de versiones. El resultado es una vista compartida y en vivo del proyecto, en lugar de una colección de terminales, ramas y pestañas del navegador desconectadas.

<img src="../docs/assets/readme/tui.png" alt="Agent Orchestrator workspace with a supervised native agent interface" width="100%" />

## Los workers ejecutan tareas bien definidas

Un worker es la unidad de ejecución de AO: una tarea, un agente de programación y un espacio de trabajo aislado. Usa **New task** cuando el trabajo ya esté claro. Describe el resultado, elige un agente y un modelo, adjunta los archivos pertinentes y trabaja con el agente desde el Chat estructurado o desde su interfaz de terminal nativa.

Puedes abrir un worker en cualquier momento para continuar la conversación, conectarte a su terminal, inspeccionar sus cambios, usar su navegador aislado, revisar su pull request o devolver feedback de CI y revisión al mismo agente. Así, cada tarea se entiende por sí sola y el trabajo en paralelo no termina mezclado en un único contexto compartido.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Agent Orchestrator" width="100%" />

## El orquestador planifica a escala de proyecto

El orquestador del proyecto es el agente permanente de planificación y coordinación de AO. Trabaja un nivel por encima de las tareas individuales: la dirección del producto, la estrategia técnica, las prioridades y la secuencia de trabajo en todo el repositorio.

Usa el orquestador para explorar una idea antes de implementarla, desarrollar enfoques de producto y técnicos, sopesar ventajas, costes y compromisos, detectar trabajo de alto impacto y convertir un objetivo ambiguo en un plan concreto. Su conversación, ligada al proyecto, conserva objetivos, decisiones, restricciones y razonamientos anteriores. Combina ese historial de planificación con el contexto del repositorio y el estado actual de AO, incluidos los workers activos, sus responsables, los pull requests, la CI y las revisiones. De este modo, la planificación se mantiene anclada tanto en el proyecto como en el trabajo que ya está en marcha.

Cuando un plan está listo para ejecutarse, el orquestador puede dividirlo en tareas bien definidas, iniciar o redirigir workers, entregar a cada uno el contexto pertinente, seguir su progreso y coordinar el trabajo posterior. El orquestador se ocupa de la planificación y la delegación. Los workers se ocupan de la implementación, las pruebas, los commits y los pull requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Agent Orchestrator coordinating multiple workers with project context" width="100%" />

## El Kanban hace que el sistema sea comprensible

Cada worker aparece en el mismo tablero en vivo, tanto si lo iniciaste desde **New task** como si lo delegó el orquestador. AO determina la posición de cada tarjeta a partir de datos de la sesión, el pull request, la CI y la revisión. Así, el Kanban se convierte en una vista operativa del proyecto:

- **Working:** workers que están implementando activamente o listos para recibir otra instrucción
- **Needs you:** sesiones bloqueadas, datos pendientes, CI fallida, cambios solicitados o señales perdidas
- **In review:** pull requests abiertos o en borrador a la espera de comprobaciones o revisiones
- **Ready to merge:** trabajo aprobado o listo para integrar; las sesiones ya integradas siguen visibles hasta que se archivan

Cada tarjeta reúne la tarea, el agente, la rama, la actividad, el pull request y el estado. Ábrela para inspeccionar la conversación o el terminal, los archivos modificados, el resumen del PR, las revisiones y la vista previa. El tablero muestra qué avanza, qué está bloqueado y dónde tendrá más impacto tu atención.

<img src="../docs/assets/readme/hero.png" alt="Agent Orchestrator Kanban showing workers grouped by live status" width="100%" />

## Un solo flujo de trabajo, de la idea a la integración

1. **Empieza en el nivel adecuado.** Entrega una tarea clara directamente a un worker o desarrolla un objetivo mayor con el orquestador del proyecto y deja que dé forma al plan.
2. **Delega trabajo concreto.** Inicia los workers tú mismo o deja que el orquestador los cree con el contexto y la responsabilidad que necesitan.
3. **Trabaja de forma aislada.** Cada worker respaldado por Git recibe su propia rama y su propio worktree. Los workers Scratch reciben directorios sin rama gestionados por AO.
4. **Supervisa el estado en vivo.** AO sigue la actividad de los agentes, los pull requests, la CI, el feedback de revisión y los conflictos de integración, y refleja esos datos en el Kanban.
5. **Cierra el ciclo de feedback.** Inspecciona cualquier worker directamente, toma decisiones de proyecto con el orquestador y devuelve los fallos accionables o comentarios de revisión al agente responsable.

AO funciona con los agentes de programación y el flujo de control de versiones que ya utilizas. Los agentes mantienen sus fortalezas propias. AO aporta el contexto del proyecto, la ejecución aislada, la coordinación y la vista operativa que les permiten funcionar como un sistema.

## Funciones destacadas

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests y revisiones con agentes</h3>
      <p>Mantén junto al worker la CI, la capacidad de integración, el estado de los revisores y las revisiones interactivas con agentes. Después, devuelve los cambios solicitados al mismo responsable.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Sesión de worker con pull request, CI y estado de revisión con agentes en Agent Orchestrator" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Navegador controlable por agentes</h3>
      <p>Previsualiza e inspecciona la aplicación local de un worker junto a su interfaz. Los perfiles del navegador están aislados por worker para que las tareas de UI en paralelo no compartan estado.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Un worker controla su vista previa aislada en el navegador integrado" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Interfaces nativas, un solo supervisor</h3>
      <p>Usa Chat estructurado o la interfaz de terminal nativa del agente mientras AO mantiene el contexto, el estado del workspace y el feedback en un solo lugar.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Agent Orchestrator" width="100%" />
    </td>
  </tr>
</table>

## Agentes compatibles

**26 agentes de programación compatibles** dentro de un único flujo supervisado.

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

[Consulta las guías de configuración de agentes →](https://aoagents.dev/docs/plugins/agents)

**Usa la interfaz que mejor encaje en cada momento: el Chat estructurado o la interfaz de terminal nativa del agente.**

## Instalación

Descarga la última aplicación de escritorio de AO para tu plataforma. AO comprueba automáticamente si hay actualizaciones.

| Plataforma            | Descarga                                                                                                                       |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| macOS (Apple silicon) | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Descargar](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Abre Agent Orchestrator y selecciona el repositorio que quieres que AO gestione. La aplicación de escritorio ejecuta el daemon por ti, así que no necesitas ninguna CLI. Consulta la [guía de instalación](https://aoagents.dev/docs/installation) para configurar las CLI de los agentes y resolver problemas.

## Informar de un bug

La forma recomendada de informar de un bug es pedir a tu agente de programación que siga la [skill de triaje de bugs](https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) del repositorio. La skill guía al agente para reproducir el problema con el código actual, recopilar diagnósticos, rastrear la ruta de código pertinente, comprobar si hay duplicados y crear o actualizar un issue detallado en GitHub.

Tanto si se lo pides a un agente local como a AO Bot en Discord, adjunta capturas de pantalla y comparte toda la información pertinente que puedas. Incluye qué ocurrió, dónde y cuándo, los pasos para reproducirlo, tu sistema operativo, la versión de AO y si sucede siempre o de forma intermitente. Así, el agente tendrá más posibilidades de reproducir el bug y presentar un informe accionable.

```text
Lee y sigue https://github.com/Untrivial-ai/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md. Reproduce e investiga este bug siguiendo el proceso de triaje y, después, crea o actualiza el issue de GitHub. Contexto: <qué ocurrió, dónde, cuándo, pasos para reproducirlo, sistema operativo, versión de AO y frecuencia>. Capturas: <adjunta las capturas disponibles>.
```

También puedes informar de un bug en el [canal de triaje de bugs de Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Menciona a `@AO Bot#8425`, describe lo ocurrido y pídele que use la skill de triaje de bugs.

```text
@AO Bot#8425 Reproduce e investiga este bug usando la skill de triaje de bugs y, después, crea o actualiza el issue de GitHub. Contexto: <qué ocurrió, dónde, cuándo, pasos para reproducirlo, sistema operativo, versión de AO y frecuencia>. Capturas: <adjunta las capturas disponibles>.
```

## Desarrollar y contribuir

Las contribuciones son bienvenidas en código, documentación, triaje, ejemplos y pruebas.

```bash
git clone https://github.com/Untrivial-ai/agent-orchestrator.git
cd agent-orchestrator
```

Empieza por la [guía de desarrollo](../docs/development.md), donde encontrarás los requisitos previos, la configuración local y los comandos de prueba. Lee [CONTRIBUTING.md](../CONTRIBUTING.md) antes de abrir un pull request y utiliza [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues) para informar de bugs o proponer funciones.

## Documentación

| Documento                                                           | Empieza aquí si necesitas                                                                                |
| ------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| [Documentación del producto](https://aoagents.dev/docs)             | Instalación, configuración de agentes y uso cotidiano del producto.                                      |
| [docs/architecture.md](../docs/architecture.md)                     | Modelo mental del backend, ciclo de vida, persistencia, CDC, derivación del estado y límites del daemon. |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Responsabilidad de los paquetes y ubicación de cada aspecto del backend.                                 |
| [docs/cli/README.md](../docs/cli/README.md)                         | Comportamiento de la CLI y correspondencia con las rutas del daemon.                                     |
| [docs/development.md](../docs/development.md)                       | Requisitos, compilación, pruebas y solución de problemas para desarrollo local.                          |
| [docs/STATUS.md](../docs/STATUS.md)                                 | Qué se distribuye actualmente en `main` y qué sigue en desarrollo.                                       |

## Sigue nuestro recorrido

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="../assets/tweet2.png" height="330" alt="Actualización del recorrido de Agent Orchestrator en X" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="../assets/tweet1.png" height="330" alt="Actualización del recorrido de Agent Orchestrator en X" />
      </a>
    </td>
  </tr>
</table>

## Comunidad

Únete a [Discord](https://discord.com/invite/UZv7JjxbwG) para pedir ayuda y hablar con otros colaboradores, sigue a [@aoagents](https://x.com/aoagents) para conocer las novedades o inicia una conversación en [GitHub Issues](https://github.com/Untrivial-ai/agent-orchestrator/issues).

## Telemetría anónima

AO utiliza métricas de uso y fiabilidad que protegen la privacidad y están diseñadas para excluir información de identificación personal y contenido de los proyectos. Estas métricas nos ayudan a entender la adopción y mejorar el producto. [Más información sobre telemetría y privacidad](../docs/telemetry.md).

## Licencia

Agent Orchestrator está disponible bajo la [licencia Apache 2.0](../LICENSE).
