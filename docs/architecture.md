# Agent Orchestrator Architecture

Agent Orchestrator is a long-running Go daemon that supervises multiple parallel AI coding agent sessions. Every session owns an isolated git worktree and one committed interface mode at a time. A TUI session runs its agent inside a tmux/conpty runtime; a Chat session runs a native protocol controller without an agent terminal runtime. A durable handoff may move a compatible native conversation between them, but both controllers are never live at once. The daemon coordinates both through the same session, lifecycle, workspace, storage, and observation boundaries.

## Table of Contents

- [Mental Model](#mental-model)
- [System Overview](#system-overview)
- [Core Architectural Principles](#core-architectural-principles)
- [Component Architecture](#component-architecture)
- [Data Flows](#data-flows)
- [Persistence and CDC](#persistence-and-cdc)
- [Status Derivation](#status-derivation)
- [Lifecycle Management](#lifecycle-management)
- [Observation Loops](#observation-loops)
- [HTTP Layer](#http-layer)
- [Terminal Multiplexing](#terminal-multiplexing)
- [Browser Runtime Bridge](#browser-runtime-bridge)

---

## Mental Model

The fundamental architecture follows a simple three-stage pipeline:

```mermaid
flowchart LR
    A[OBSERVE<br/>External Facts] --> B[UPDATE<br/>Durable Facts]
    B --> C[DERIVE<br/>Display Status / ACT]

```

**Key insight:** Display status is never stored. It is computed at read time from durable facts.

### Durable Session Facts

The only persistent session state is:

- `activity_state` — What the agent last reported (`active`, `idle`, `waiting_input`, `blocked`, `exited`). `waiting_input` is an agent at an empty prompt awaiting its next instruction; `blocked` is an agent stopped on a pending permission/approval decision — automation must never inject input into a blocked session.
- `is_terminated` — Whether the session should be treated as over
- `session_mode` plus its runtime/provider handle and generation — The currently committed controller epoch
- `session_interface_transitions` — Durable checkpoints for an in-progress or completed TUI↔Chat handoff
- PR facts — `pr`, `pr_checks`, `pr_comment` tables

### What is NOT Durable

Display status like `working`, `needs_input`, `ci_failed`, `mergeable` are **computed at read time** by the service layer from the durable facts above.

---

## System Overview

```mermaid
graph TB
    subgraph Frontend
        FE[Electron + React UI]
        Mobile[Expo + React Native UI]
        CLI[ao CLI]
    end

    subgraph HTTP["HTTP Daemon (127.0.0.1)"]
        Controllers[REST Controllers]
        SSE[SSE Events]
        Terminal[Terminal WebSocket]
    end

    subgraph Core["Core Services"]
        SessionSvc[Session Service]
        ProjectSvc[Project Service]
        PRSvc[PR Service]
        ReviewSvc[Review Service]
        SessionMgr[Session Manager]
        ChatSvc[Chat Service]
        LCM[Lifecycle Manager]
    end

    subgraph Observe["Observation Layer"]
        SCMObserver[SCM Observer]
        Reaper[Runtime Reaper]
    end

    subgraph Storage["Persistence Layer"]
        SQLite[(SQLite DB)]
        CDC[CDC Poller]
        Broadcaster[Event Broadcaster]
    end

    subgraph Adapters["Adapters"]
        AgentAdapter[Agent Adapters]
        RuntimeAdapter[Runtime tmux/conpty]
        ChatDriver[Native Chat / ACP Drivers]
        WorkspaceAdapter[Workspace git worktree]
        SCMAdapter[SCM GitHub]
    end

    FE -->|REST/SSE| Controllers
    Mobile -->|Authenticated LAN REST/SSE| Controllers
    Mobile -->|Authenticated mux| Terminal
    CLI -->|REST| Controllers
    Controllers --> SessionSvc
    Controllers --> ProjectSvc
    Controllers --> PRSvc

    SessionSvc --> SessionMgr
    SessionMgr --> ChatSvc
    SessionMgr --> LCM
    SessionMgr --> AgentAdapter
    SessionMgr --> RuntimeAdapter
    SessionMgr --> WorkspaceAdapter
    ChatSvc --> ChatDriver

    LCM --> SQLite
    LCM --> AgentAdapter

    SCMObserver --> SCMAdapter
    SCMObserver --> SQLite
    SCMObserver --> LCM

    Reaper --> RuntimeAdapter
    Reaper --> SQLite
    Reaper --> LCM

    CDC -->|poll| SQLite
    CDC --> Broadcaster
    Broadcaster --> SSE

    Terminal --> RuntimeAdapter

```

---

## Core Architectural Principles

### 1. Port-Based Design

Core code never depends on concrete implementations. All external systems are accessed through port interfaces defined in `backend/internal/ports/`:

```mermaid
graph LR
    Core[Core Services] -->|consumes| Ports[Port Interfaces]
    Adapters[Adapters] -->|implement| Ports
    External[External Systems] -->|wrapped by| Adapters

```

### 2. Durable Facts, Derived Status

Storage layer persists minimal facts. Service layer computes display status on-demand:

```mermaid
flowchart LR
    SQLite[(SQLite)] -->|raw facts| Service[Session Service]
    Service -->|compute| Status[Display Status]
    Service -->|enrich| UI[Dashboard/UI]

    SQLite -->|activity_state| Service
    SQLite -->|is_terminated| Service
    SQLite -->|PR facts| Service
    SQLite -->|runtime_handle| Service

```

### 3. Observer Pattern

Observation is separated from action:

- **Observe layer** — SCM Observer, Runtime Reaper poll external state
- **Lifecycle layer** — Reduces observations into durable facts
- **Service layer** — Computes display status from facts

### 4. Change Data Capture

All durable changes flow through a CDC pipeline:

```mermaid
flowchart LR
    DB[(SQLite)] -->|triggers| ChangeLog[change_log table]
    ChangeLog -->|tail| Poller[CDC Poller]
    Poller -->|Event| Broadcaster[Event Broadcaster]
    Broadcaster -->|fan-out| Subscribers[Subscribers]
    Subscribers -->|SSE| Clients[Dashboard Clients]

```

---

## Component Architecture

### Package Layout

```
backend/internal/
├── domain/              # Shared vocabulary and durable fact records
├── ports/               # Inbound/outbound interfaces
├── service/             # Controller-facing services
│   ├── project/         # Project CRUD
│   ├── session/         # Session read-model assembly
│   ├── chat/            # Runtime-less Chat controllers + durable projection
│   ├── pr/              # PR observation service
│   └── review/          # Code review service
├── session_manager/     # Internal session command engine
├── lifecycle/           # Durable session fact reducer
├── observe/             # Observation loops
│   ├── scm/             # SCM (GitHub) observer
│   └── reaper/          # Runtime liveness observer
├── storage/             # SQLite persistence
│   └── sqlite/          # DB, migrations, queries, stores
├── cdc/                 # Change-log poller and broadcaster
├── httpd/               # HTTP API, controllers, terminal mux
├── terminal/            # Terminal session protocol
├── adapters/            # Concrete adapter implementations
│   ├── agent/           # 23+ agent harnesses
│   ├── chatdriver/      # Native provider protocols and reusable ACP transport
│   ├── runtime/         # tmux/conpty runtimes
│   ├── workspace/       # git worktree
│   ├── scm/             # GitHub
│   └── tracker/         # GitHub tracker
├── daemon/              # Production wiring
└── config/              # Environment-based configuration
```

### Core Data Flow

```mermaid
sequenceDiagram
    participant UI as Dashboard
    participant HTTP as HTTP Controller
    participant Svc as Session Service
    participant Mgr as Session Manager
    participant LCM as Lifecycle Manager
    participant Agent as Agent Adapter
    participant Runtime as Runtime Adapter
    participant ChatSvc as Chat Service
    participant ChatDriver as Chat Driver
    participant WS as Workspace Adapter
    participant DB as SQLite
    participant CDC as CDC Broadcaster

    UI->>HTTP: POST /sessions
    HTTP->>Svc: Spawn(config)
    Svc->>Mgr: Spawn(config)

    Mgr->>Mgr: Resolve initial mode
    alt initial mode = chat
        Mgr->>ChatSvc: Preflight binary/auth/protocol
        ChatSvc->>ChatDriver: Probe installed provider
    else initial mode = tui
        Mgr->>Runtime: Validate runtime prerequisites
    end

    Note over Mgr: 1. Create session row
    Mgr->>DB: Insert session
    DB->>CDC: trigger change_log
    CDC->>UI: SSE session.created

    Note over Mgr: 2. Create workspace
    Mgr->>WS: Create(project, branch)
    WS->>WS: git worktree add

    alt persisted mode = tui
        Note over Mgr: 3a. Launch terminal controller
        Mgr->>Runtime: Create(session)
        Runtime->>Runtime: Start tmux/conpty
        Mgr->>Agent: GetLaunchCommand()
        Agent-->>Mgr: launch command
        Mgr->>Runtime: Execute(agent command)
    else persisted mode = chat
        Note over Mgr: 3b. Launch native Chat controller
        Mgr->>ChatSvc: StartChat(session, worktree, harness)
        ChatSvc->>ChatDriver: Start or resume provider conversation
        Note over Runtime: No agent runtime handle is created
    end

    Note over Mgr: 4. Mark spawned
    Mgr->>LCM: MarkSpawned(handle)
    LCM->>DB: Update activity_state
    DB->>CDC: trigger change_log
    CDC->>UI: SSE session.updated

    Mgr-->>Svc: Session(created)
    Svc-->>HTTP: Session response
    HTTP-->>UI: 201 Created
```

---

## Data Flows

### Session Spawn Flow

```mermaid
flowchart TD
    Start([User spawns session]) --> Validate[Validate project config and explicit mode]
    Validate --> InitialMode{Resolved initial mode}
    InitialMode -->|chat| Preflight[Probe native Chat driver]
    InitialMode -->|tui| RuntimePreflight[Validate runtime prerequisites]
    Preflight --> CreateRow[Create session row in SQLite]
    RuntimePreflight --> CreateRow
    CreateRow --> Trigger1[CDC: session.created]
    CreateRow --> CreateWS[Create git worktree]
    CreateWS --> LaunchMode{Persisted mode}
    LaunchMode -->|tui| CreateRT[Launch runtime tmux/conpty]
    CreateRT --> GetCmd[Get agent launch command]
    GetCmd --> ExecAgent[Execute agent in runtime]
    LaunchMode -->|chat| ChatController[Start or resume provider controller]
    ChatController --> Fence[Claim controller generation]
    ExecAgent --> MarkSpawned[MarkSpawned in LCM]
    Fence --> MarkSpawned
    MarkSpawned --> Trigger2[CDC: session.updated]
    Trigger1 --> Done
    Trigger2 --> Done([Session running])

```

### Session Interface Handoff

An interface switch is a controller replacement inside the existing AO session,
not a new session. The session id, project, worktree, branch, lifecycle facts,
PR ownership, and provider-native conversation id stay the same. Only the
mode-owned controller changes.

The generic coordinator lives in `session_manager`; providers opt in through the
small `AgentInterfaceHandoff` capability only after their TUI resume id and Chat
protocol id are proven to name the same native conversation. Claude Code and
Codex currently satisfy that contract. Merely having a Chat/ACP driver is not
enough to enable switching for another harness.

```mermaid
sequenceDiagram
    participant Client
    participant Manager as Session Manager
    participant Lifecycle as Lifecycle Manager
    participant DB as SQLite
    participant Source as Current Controller
    participant Target as Target Controller

    Client->>Manager: POST interface-transition(target, policy)
    Manager->>DB: Claim one active transition
    alt source = Chat
        Manager->>Source: Arm handoff; close intake and queue dispatch
    else source = TUI
        Manager->>Source: Gate new terminal input
    end
    Manager->>Target: Preflight binary/auth/protocol
    alt policy = drain
        Manager->>Source: Finish accepted work
    else policy = interrupt
        Source->>DB: Cancel queued Chat turns
        Manager->>Source: Cancel active provider turn
    end
    Manager->>Source: Stop and wait for shutdown
    Manager->>Lifecycle: CommitControllerEpoch(source, target, native id)
    Lifecycle->>DB: CAS mode + clear old generation/handles + idle fact
    Manager->>Target: Native resume(same conversation id)
    Manager->>DB: Persist new handle/generation; complete transition
    DB-->>Client: session_updated CDC invalidation
```

The session row is the commit point. If target startup fails, the coordinator
CASes the row back and resumes the source. If the daemon dies mid-handoff, boot
reconciliation marks the interrupted transition for recovery and restores the
controller named by the last committed `session_mode`. Lifecycle/automation
messages received during the no-controller gap are held in a durable outbox and
delivered through whichever controller ultimately owns the session. Terminal
transition paths, transient delivery failures, and daemon restarts all retain
the message for retry; Chat retries carry a stable idempotency key. Old Chat
events are fenced by controller generation; old TUI hooks are fenced by runtime
launch id.

`drain` is loss-minimizing and may wait on an approval or user-input request;
`interrupt` synchronously closes source intake and queue dispatch at transition
acceptance. After target preflight succeeds, it settles queued Chat turns and
then sends the provider's active-turn cancellation, allows a short transcript
flush, and stops the source. The reversible first phase preserves queued work if
the target is unavailable; its dispatch fence prevents a completion callback
from promoting that work during preflight or provider cancellation. Files and
completed provider context survive.
There is no provider-neutral way to migrate a currently executing tool call or a
detached background process, and AO does not synthesize terminal screen output
into structured Chat history.

For TUI drains, AO gates new terminal input before checking quiescence. Agent
adapters that can interpret their rendered TUI report work state and composer
occupancy as separate ephemeral facts. The runtime side of that contract must
provide the current rendered viewport with ANSI cell styles: tmux uses styled
`capture-pane`, while the Windows ConPTY host maintains a VT cell model beside
its historical replay ring. AO accepts only repeated observations of an idle
surface with an empty composer, held across the settle window; a visible draft
fails with the source untouched and requires the user to submit, clear, or
explicitly discard it. Adapter/runtime pairs without rendered-surface support
retain the causally newer idle-fact or legacy terminal-idle fallback. An
unverified idle state has a bounded proof window; active work or a user-paced
decision remains unbounded.

### Observation Flow

```mermaid
flowchart TD
    subgraph SCM["SCM Observer Loop"]
        Poll1[Poll PRs every 30s]
        Poll1 --> Fetch[Fetch from GitHub API]
        Fetch --> Diff[Semantic diff vs local]
        Diff --> Changed{Changed?}
        Changed -->|Yes| WritePR[Write PR/check/comment]
        Changed -->|No| Wait1[Wait for tick]
        WritePR --> NotifyLCM[Notify Lifecycle Manager]
        NotifyLCM --> Trigger1[CDC event]
        Trigger1 --> Wait1
        Wait1 --> Poll1
    end

    subgraph Reaper["Runtime Reaper Loop"]
        Poll2[Poll every 5s]
        Poll2 --> Probe[Probe each runtime]
        Probe --> Report[Report fact to LCM]
        Report --> Trigger2[CDC event]
        Trigger2 --> Wait2[Wait for tick]
        Wait2 --> Poll2
    end

    LCM[Lifecycle Manager] -->|consumes| NotifyLCM
    LCM -->|consumes| Report

```

### Feedback Routing Flow

```mermaid
sequenceDiagram
    participant SCM as SCM Observer
    participant LCM as Lifecycle Manager
    participant Dispatch as Mode-aware Messenger
    participant TUI as Runtime Messenger
    participant Chat as Chat Controller

    SCM->>SCM: Observe PR comment
    SCM->>LCM: ApplySCMObservation()
    LCM->>LCM: Detect actionable feedback
    LCM->>Dispatch: Send(feedback)

    SCM->>SCM: Observe CI failure
    SCM->>LCM: ApplySCMObservation()
    LCM->>LCM: Detect actionable feedback
    LCM->>Dispatch: Send(CI failure)

    SCM->>SCM: Observe merge conflict
    SCM->>LCM: ApplySCMObservation()
    LCM->>LCM: Detect actionable feedback
    LCM->>Dispatch: Send(merge conflict)

    alt session mode = tui
        Dispatch->>TUI: Send through runtime handle
    else session mode = chat
        Dispatch->>Chat: Enqueue native provider turn
    end
```

---

## Persistence and CDC

### SQLite Schema

```mermaid
erDiagram
    projects ||--o{ sessions : owns
    projects ||--o| conversations : owns_orchestrator_narrative
    sessions ||--o| conversations : owns_worker_narrative
    sessions ||--o{ session_interface_transitions : records_controller_handoffs
    session_interface_transitions ||--o{ session_interface_transition_messages : holds_messages_during_gap
    conversations ||--o{ conversation_turns : contains
    conversations ||--o{ conversation_messages : contains
    conversations ||--o{ conversation_activities : contains
    sessions ||--o{ pull_requests : owns
    pull_requests ||--o{ pr_checks : has
    pull_requests ||--o{ pr_review_threads : has
    pull_requests ||--o{ pr_comments : has
    sessions ||--o{ notifications : has
    change_log }|--|| projects : tracks
    change_log }|--|| sessions : tracks
    change_log }|--|| pull_requests : tracks

    projects {
        string id PK
        string name
        string repo
        jsonb config
    }

    sessions {
        string id PK
        string project_id FK
        string harness
        string session_mode
        string runtime_handle_id
        string provider_conversation_id
        string controller_generation
        string activity_state
        boolean is_terminated
        jsonb metadata
    }

    conversations {
        string id PK
        string scope
        string project_id FK
        string session_id FK
        string current_session_id FK
        integer latest_sequence
    }

    pull_requests {
        string id PK
        string session_id FK
        integer number
        string state
        string title
        boolean draft
        boolean mergeable
    }

    pr_checks {
        string id PK
        string pr_id FK
        string name
        string status
        string conclusion
    }

    change_log {
        bigint seq PK
        string table_name
        string row_id
        string operation
        jsonb old_data
        jsonb new_data
    }
```

### CDC Pipeline

```mermaid
flowchart LR
    DB[(SQLite)] -->|INSERT/UPDATE/DELETE| Trigger[DB Trigger]
    Trigger -->|append| ChangeLog[change_log]
    ChangeLog -->|poll| Poller[CDC Poller]
    Poller -->|decode| Decoder[Event Decoder]
    Decoder -->|Event| Broadcaster[Broadcaster]
    Broadcaster -->|callback| Sub1[Terminal Fanout]
    Broadcaster -->|callback| Sub2[SSE Writer]
    Broadcaster -->|callback| Sub3[Cache Invalidation]

    Poller -->|watermark| Watermark[seq tracking]
    Watermark -->|resume position| Poller

```

---

## Status Derivation

### Display Status Precedence

The `service.Session` computes display status from durable facts using this precedence (highest to lowest):

```mermaid
flowchart TD
    CheckTerm{is_terminated?}
    CheckTerm -->|Yes| PRMerged{PR merged?}
    CheckTerm -->|No| CheckWait{activity_state in<br/>waiting_input, blocked?}

    PRMerged -->|Yes| Merged[merged]
    PRMerged -->|No| Terminated[terminated]

    CheckWait -->|Yes| NeedsInput[needs_input]
    CheckWait -->|No| CheckPR{Has PR facts?}

    CheckPR -->|Yes| PRPipeline[PR Pipeline Check]
    CheckPR -->|No| CheckActive{activity_state<br/>== active?}

    PRPipeline --> PRState{PR State}
    PRState -->|ci failed| CIFailed[ci_failed]
    PRState -->|draft| Draft[draft]
    PRState -->|changes requested| Changes[changes_requested]
    PRState -->|not mergeable| Conflict[merge_conflict]
    PRState -->|mergeable| Mergeable[mergeable]
    PRState -->|approved| Approved[approved]
    PRState -->|review pending| ReviewPending[review_pending]
    PRState -->|open| PROpen[pr_open]

    CheckActive -->|Yes| Working[working]
    CheckActive -->|No| CheckSignal{Signal capable<br/>&& no signal?}

    CheckSignal -->|Yes| NoSignal[no_signal]
    CheckSignal -->|No| Idle[idle]

```

### PR Pipeline States

```mermaid
flowchart LR
    PR[Open PR] --> CI{CI Status}
    CI -->|failing| CIFailed[ci_failed]
    CI -->|pending| CIPending[ci_pending]
    CI -->|passing| Review{Reviews}

    Review -->|changes requested| Changes[changes_requested]
    Review -->|approved| Mergeable{Mergeable?}

    Mergeable -->|conflict| Conflict[merge_conflict]
    Mergeable -->|yes| Merged[Mergeable]

    PR -.->|draft| Draft[Draft State]

```

---

## Lifecycle Management

### Lifecycle Manager Responsibilities

The `lifecycle.Manager` is the **canonical write path** for all session lifecycle facts:

```mermaid
flowchart TD
    subgraph Inputs["Observation Inputs"]
        RuntimeObs[TUI Runtime Observations]
        ActivitySignals[Agent Activity Signals]
        ChatSignals[Chat Controller Signals]
        SCMObs[SCM Observations]
    end

    subgraph LCM["Lifecycle Manager"]
        Reducer[Fact Reducer]
        StateMachine[Activity State Machine]
        Termination[Termination Logic]
        Nudge[Agent Nudge Engine]
    end

    subgraph Outputs["Durable Facts"]
        ActivityState[activity_state]
        IsTerminated[is_terminated]
        PRFacts[PR Facts Table]
    end

    RuntimeObs --> Reducer
    ActivitySignals --> Reducer
    ChatSignals --> Reducer
    SCMObs --> Reducer

    Reducer --> StateMachine
    StateMachine --> Termination
    Termination --> ActivityState
    Termination --> IsTerminated

    SCMObs --> Nudge
    Nudge -->|route| Agent[Agent Adapter]

```

### Session State Machine

```mermaid
stateDiagram-v2
    [*] --> Spawning: Spawn()
    Spawning --> Active: MarkSpawned
    Active --> Idle: activity_state = idle
    Active --> Working: activity_state = active
    Active --> Waiting: activity_state = waiting_input / blocked
    Active --> Exited: activity_state = exited
    Working --> Active: work completes
    Waiting --> Active: user responds
    Idle --> Active: agent starts work
    Exited --> Terminated: process exit
    Active --> Terminated: Kill()
    Waiting --> Terminated: Kill()
    Idle --> Terminated: Kill()
    Terminated --> [*]

    note right of Active
        Agent is working
        TUI runtime or Chat controller alive
    end note

    note right of Waiting
        Agent needs input
        Waiting for user
    end note

    note right of Terminated
        Session over
        Mode-owned controller cleaned up
    end note
```

### Termination Guardrails

The lifecycle manager only terminates when **all** conditions are met:

```mermaid
flowchart TD
    Check{Can terminate?}
    Check -->|No| Keep[Keep running]

    Check -->|Yes| AllDead{Runtime AND<br/>process dead?}
    AllDead -->|No| Keep
    AllDead -->|Yes| NoRecent{No recent<br/>activity?}
    NoRecent -->|No| Keep
    NoRecent -->|Yes| NoPR{No merged PR<br/>ownership?}
    NoPR -->|No| Keep
    NoPR -->|Yes| Terminate[Mark terminated]

    Terminate --> Cleanup[Trigger cleanup]
    Cleanup --> CDC[CDC event]
    CDC --> UI[Dashboard update]

```

**Key principle:** Failed probes are NOT proof of death. A session is only terminated when the runtime and process are **both** clearly dead and recent activity doesn't contradict that.

---

## Observation Loops

### SCM Observer

```mermaid
flowchart TD
    Start([Observer Start]) --> Immediate[Immediate Poll]
    Immediate --> Loop{Tick every 30s}

    Loop --> ListRepos[List active repos]
    ListRepos --> CheckCreds{Credentials<br/>available?}
    CheckCreds -->|No| Disabled[Disabled mode]
    CheckCreds -->|Yes| Fetch[Fetch PRs via ETags]

    Fetch --> ListPRs[List open PRs]
    ListPRs --> Discover[Discover new PRs]
    Discover --> FetchDetailed[Fetch detailed PR data]
    FetchDetailed --> FetchChecks[Fetch CI checks]
    FetchChecks --> FetchReviews[Fetch review threads]

    FetchReviews --> Write[Write to SQLite]
    Write --> Notify[Notify Lifecycle]
    Notify --> Trigger[CDC event]

    Disabled --> Loop
    Trigger --> Loop

```

### Runtime Reaper

```mermaid
flowchart TD
    Start([Reaper Start]) --> Loop{Tick every 5s}

    Loop --> List[List non-terminated<br/>sessions]
    List --> ForEach[For each session]

    ForEach --> GetHandle{Has runtime<br/>handle?}
    GetHandle -->|No, including Chat| Skip[Skip runtime probe]
    GetHandle -->|Yes| Probe[Probe runtime]

    Probe --> Result{Probe result}
    Result -->|Error| ReportFailed[Report ProbeFailed]
    Result -->|Alive| ReportAlive[Report ProbeAlive]
    Result -->|Dead| ReportDead[Report ProbeDead]

    ReportFailed --> Apply[ApplyRuntimeObservation]
    ReportAlive --> Apply
    ReportDead --> Apply

    Apply --> LCM[Lifecycle Manager]
    LCM --> Update[Update facts]
    Update --> CDC[CDC event]

    Skip --> NextSession{More sessions?}
    CDC --> NextSession
    NextSession -->|Yes| ForEach
    NextSession -->|No| Loop

```

### Observation Integration

```mermaid
flowchart LR
    subgraph External["External State"]
        GitHub[GitHub API]
        Runtimes[tmux/conpty]
    end

    subgraph Observers["Observation Layer"]
        SCM[SCM Observer]
        Reaper[Runtime Reaper]
    end

    subgraph Core["Core Processing"]
        LCM[Lifecycle Manager]
        PRMgr[PR Manager]
    end

    subgraph Storage["Persistence"]
        SQLite[(SQLite)]
    end

    GitHub --> SCM
    Runtimes --> Reaper

    SCM --> PRMgr
    PRMgr --> SQLite
    PRMgr --> LCM

    Reaper --> LCM
    LCM --> SQLite

```

---

## HTTP Layer

### API Structure

```mermaid
flowchart TD
    subgraph HTTPD["HTTP Daemon"]
        Router[Router + Middleware]

        Router --> API[REST API]
        Router --> Events[SSE Events]
        Router --> Terminal[Terminal WebSocket]
    end

    subgraph Controllers["Controllers"]
        Sessions[Sessions Controller]
        Projects[Projects Controller]
        PRs[PRs Controller]
        Reviews[Reviews Controller]
    end

    subgraph Services["Services"]
        SessionSvc[Session Service]
        ProjectSvc[Project Service]
        PRSvc[PR Service]
        ReviewSvc[Review Service]
    end

    API --> Sessions
    API --> Projects
    API --> PRs
    API --> Reviews

    Sessions --> SessionSvc
    Projects --> ProjectSvc
    PRs --> PRSvc
    Reviews --> ReviewSvc

    Events -->|subscribe| CDC[CDC Broadcaster]
    Terminal --> TerminalMux[Terminal Manager]

```

### Multi-Listener Architecture (Loopback + LAN)

The daemon runs two independent HTTP listeners sharing the same chi router:

1. **Primary (Loopback) Listener** — binds `127.0.0.1:3001` with no authentication. All existing daemon operations (CLI, desktop app) use this listener.
2. **LAN Listener** (Connect Mobile) — an opt-in second listener that binds `0.0.0.0:3011` (or ephemeral fallback) **only when explicitly enabled** by the user through the desktop app's Settings. It wraps the shared router in bearer-password authentication middleware, serves app API routes to mobile clients, but never exposes loopback-gated control routes (`/shutdown`, telemetry, mobile control commands). All traffic is plaintext HTTP on a home network only, by deliberate security decision — see `docs/adr/0001-lan-listener-for-mobile.md` for rationale and threat model. Auth state (hashed password, per-source lockout) is persisted to `~/.ao/mobile/config.json` and restored on daemon boot.

The mobile app is a second thin renderer over those same session resources. It
branches on the session's persisted `mode`: TUI attaches the existing mux PTY,
while Chat reads the paged conversation projection and uses the durable CDC SSE
stream only for targeted invalidation/reconnect. Sends, approvals, input,
provider configuration, compaction, rollback, and shell creation remain daemon
commands; no provider or lifecycle policy is implemented in React Native.

For implementation details and security model, consult `docs/adr/0001-lan-listener-for-mobile.md` and the glossary in `CONTEXT.md`.

### Request Flow

```mermaid
sequenceDiagram
    participant Client
    participant Router
    participant Controller
    participant Service
    participant Manager
    participant Store
    participant DB

    Client->>Router: POST /api/v1/sessions
    Router->>Router: Middleware (auth, logging)
    Router->>Controller: handler(w, r)
    Controller->>Controller: decode JSON
    Controller->>Service: Spawn(config)
    Service->>Manager: Spawn(config)
    Manager->>Manager: Resolve mode and preflight its controller
    Manager->>Store: Create session
    Store->>DB: INSERT INTO sessions
    DB->>Store: session record
    Store->>Manager: session record
    Manager->>Manager: Create and provision workspace
    alt mode = tui
        Manager->>Manager: Launch terminal runtime/controller
    else mode = chat
        Manager->>Manager: Launch runtime-less Chat controller
    end
    Manager->>Service: Session response
    Service->>Controller: enriched session
    Controller->>Controller: encode JSON
    Controller->>Client: 201 Created + Session
```

---

## Terminal Multiplexing

The mux is the primary agent controller only for TUI-mode sessions. Chat-mode
sessions have no agent runtime handle and never attach their provider through
tmux. They may still open session-scoped shell terminals as a worktree escape
hatch; those shells are separate resources and do not become the agent
controller.

### Terminal Architecture

```mermaid
flowchart TD
    subgraph Frontend
        Browser[Browser Terminal]
    end

    subgraph HTTPD
        WS[WebSocket Handler]
    end

    subgraph Terminal
        Mux[Terminal Mux]
        Sessions[Session States]
    end

    subgraph Runtime
        TMux[tmux Runtime]
        ConPTY[conpty Runtime]
    end

    Browser -->|WebSocket| WS
    WS -->|attach| Mux
    Mux --> Sessions
    Sessions -->|create| TMux
    Sessions -->|create| ConPTY

    TMux -->|PTY attach| Mux
    ConPTY -->|loopback dial| Mux

    Mux -->|frame| WS
    WS -->|binary| Browser

```

### Attach Flow

```mermaid
sequenceDiagram
    participant Client as Browser
    participant WS as WebSocket Handler
    participant Mux as Terminal Mux
    participant Runtime as tmux/conpty

    Client->>WS: WebSocket upgrade
    WS->>Mux: Attach(session, rows, cols)
    Mux->>Runtime: Attach(handle, rows, cols)

    Runtime->>Runtime: Create PTY
    Runtime->>Runtime: Spawn tmux attach

    loop Data Loop
        Runtime->>Mux: PTY output
        Mux->>WS: Binary frame
        WS->>Client: WebSocket message

        Client->>WS: User input
        WS->>Mux: Input frame
        Mux->>Runtime: Write to PTY
    end

    Client->>WS: Close
    WS->>Mux: Detach
    Mux->>Runtime: Close PTY
```

## Browser Runtime Bridge

Browser automation uses a dedicated local socket (`browser.sock` on Unix,
`ao-browser[-dev]` named pipe on Windows) between the daemon and Electron. The
daemon owns command authorization/correlation; Electron owns the actual browser
targets. Commands never use the supervisor liveness socket and never enable an
unauthenticated remote-debugging port.

Electron attaches its debugger directly to the selected session's
`WebContentsView`, so the protocol transport cannot enumerate or attach to the
AO renderer or a different session. The loopback `/api/v1/browser` surface is
blocked entirely on the opt-in LAN listener.

Request observation is an explicit, temporary browser command rather than a
standing debugger feature. Capture is off by default, bound to the active tab
that starts it, limited to 200 in-memory metadata entries, and automatically
expires within at most five minutes. AO never requests or stores request or
response bodies; it allowlists safe headers and redacts URL credentials,
fragments, and query values. Closing the tab, ending the session, or shutting
down Electron disables and discards the capture.

---

## Load-Bearing Rules

These rules are **load-bearing** — changing them breaks fundamental architectural assumptions:

1. **Never store display status** — Status is derived from durable facts at read time
2. **Never treat failed probes as death** — A failed probe is a fact, not a termination signal
3. **Never force-delete dirty worktrees** — User data safety over cleanup convenience
4. **All app state under ~/.ao** — No OS-default app-data locations
5. **Daemon binds to 127.0.0.1 only** — No network exposure, ever
6. **CLI is thin** — All logic lives in the daemon, CLI is just an HTTP client
7. **CDC is source-truth for events** — DB triggers write to change_log, poller fans out
8. **Adapters are leaves** — Adapters never import core packages, only ports and domain
9. **Hooks are gitignored** — Every file an adapter writes must be in .gitignore
10. **Migrations never change** — Add new migrations, never modify existing ones

---

## Summary

Agent Orchestrator's architecture is designed around:

- **Separation of concerns** — Observation, persistence, and display are distinct layers
- **Port-based design** — Core code depends on interfaces, not implementations
- **Durable minimalism** — Store only facts, compute everything else
- **Event-driven updates** — CDC broadcasts changes to all subscribers
- **Isolation** — Each session owns a worktree and exactly one live mode-specific controller, including across handoffs
- **Safety** — Conservative termination, path validation, gitignored hooks

This architecture enables parallel AI agents to work safely while maintaining complete visibility and control.
