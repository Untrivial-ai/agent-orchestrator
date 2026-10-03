# Agent Orchestrator architecture

Agent Orchestrator is a long-running Go daemon that supervises multiple
parallel AI coding-agent sessions. Project sessions own isolated Git
worktrees. Projectless standalone workers own AO-managed plain-directory
workspaces. Every session uses one interface mode at a time.

A TUI session runs its agent inside a native PTY/ConPTY runtime, with tmux as a
legacy or fallback runtime. A Chat session runs a native protocol controller
without an agent terminal runtime. Codex and all ACP Chat processes run in
detached per-session hosts, so replacing the daemon or desktop app does not
stop an in-flight turn. The ACP host also preserves connection setup, JSON-RPC
correlation, pending interactions, and acknowledged prompt replay while the
replacement daemon rebuilds its typed controller.

A durable handoff can move a compatible native conversation between TUI and
Chat, but both controllers are never live at once. The daemon coordinates both
modes through the same session, lifecycle, workspace, storage, and observation
boundaries.

## Table of contents

- [Mental model](#mental-model)
- [System overview](#system-overview)
- [Core architectural principles](#core-architectural-principles)
- [Component architecture](#component-architecture)
- [Data flows](#data-flows)
- [Persistence and CDC](#persistence-and-cdc)
- [Status derivation](#status-derivation)
- [Lifecycle management](#lifecycle-management)
- [Observation loops](#observation-loops)
- [HTTP layer](#http-layer)
- [Terminal multiplexing](#terminal-multiplexing)
- [Browser runtime bridge](#browser-runtime-bridge)

---

## Mental model

The fundamental architecture follows a simple three-stage pipeline:

```mermaid
flowchart LR
    A[OBSERVE<br/>External Facts] --> B[UPDATE<br/>Durable Facts]
    B --> C[DERIVE<br/>Display Status / ACT]

```

**Key insight:** Display status is never stored. It is computed at read time from durable facts.

### Durable session facts

The only persistent session state is:

- `activity_state`: What the agent last reported (`active`, `idle`,
  `waiting_input`, `blocked`, `exited`). `waiting_input` is an agent at an
  empty prompt awaiting its next instruction. `blocked` is an agent stopped on
  a pending permission or approval decision. Automation must never inject input
  into a blocked session.
- `is_terminated`: Whether the session should be treated as over.
- `session_mode` plus its runtime/provider handle and generation: The currently
  committed controller epoch.
- `session_interface_transitions`: Durable checkpoints for an in-progress or
  completed TUI↔Chat handoff.
- PR facts: `pr`, `pr_checks`, and `pr_comment` tables.

### What is not durable

Display status like `working`, `needs_input`, `ci_failed`, `mergeable` are **computed at read time** by the service layer from the durable facts above.

---

## System overview

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
        RuntimeAdapter[Runtime native PTY / ConPTY / tmux]
        ChatDriver[Native Chat / ACP Drivers]
        WorkspaceAdapter[Git worktree / standalone directory]
        SCMAdapter[SCM GitHub/GitLab]
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

## Core architectural principles

### 1. Port-based design

Core code never depends on concrete implementations. All external systems are accessed through port interfaces defined in `backend/internal/ports/`:

```mermaid
graph LR
    Core[Core Services] -->|consumes| Ports[Port Interfaces]
    Adapters[Adapters] -->|implement| Ports
    External[External Systems] -->|wrapped by| Adapters

```

### 2. Durable facts, derived status

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

### 3. Observer pattern

Observation is separated from action:

- **Observe layer:** SCM Observer and Runtime Reaper poll external state.
- **Lifecycle layer:** Reduces observations into durable facts.
- **Service layer:** Computes display status from facts.

### 4. Change data capture

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

## Component architecture

### Package layout

```
backend/internal/
├── domain/              # Shared vocabulary and durable fact records
├── ports/               # Inbound/outbound interfaces
├── service/             # Controller-facing services
│   ├── project/         # Project CRUD
│   ├── session/         # Session read-model assembly
│   ├── chat/            # Chat controllers, persistent provider hosts + durable projection
│   ├── pr/              # PR observation service
│   └── review/          # Code review service
├── session_manager/     # Internal session command engine
├── lifecycle/           # Durable session fact reducer
├── observe/             # Observation loops
│   ├── scm/             # SCM (GitHub/GitLab) observer
│   └── reaper/          # Runtime liveness observer
├── storage/             # SQLite persistence
│   └── sqlite/          # DB, migrations, queries, stores
├── cdc/                 # Change-log poller and broadcaster
├── httpd/               # HTTP API, controllers, terminal mux
├── terminal/            # Terminal session protocol
├── adapters/            # Concrete adapter implementations
│   ├── agent/           # 23+ agent harnesses
│   ├── chatdriver/      # Native provider protocols and reusable ACP transport
│   ├── runtime/         # native PTY/ConPTY (legacy/fallback tmux) runtimes
│   ├── workspace/       # git worktree and standalone-directory adapters
│   ├── scm/             # GitHub/GitLab
│   └── tracker/         # GitHub/GitLab trackers
├── daemon/              # Production wiring
└── config/              # Environment-based configuration
```

### Core data flow

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
    alt project session
        Mgr->>WS: Create(project, branch)
        WS->>WS: git worktree add
    else standalone worker
        Mgr->>WS: Create AO-managed directory
    end

    alt persisted mode = tui
        Note over Mgr: 3a. Launch terminal controller
        Mgr->>Runtime: Create(session)
        Runtime->>Runtime: Start native PTY/ConPTY (legacy/fallback tmux)
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

## Data flows

### Session spawn flow

```mermaid
flowchart TD
    Start([User spawns session]) --> Scope{Project attached?}
    Scope -->|yes| Validate[Validate project config and explicit mode]
    Scope -->|no, worker only| ValidateStandalone[Validate standalone mode]
    Validate --> InitialMode{Resolved initial mode}
    ValidateStandalone --> InitialMode
    InitialMode -->|chat| Preflight[Probe native Chat driver]
    InitialMode -->|tui| RuntimePreflight[Validate runtime prerequisites]
    Preflight --> CreateRow[Create session row in SQLite]
    RuntimePreflight --> CreateRow
    CreateRow --> Trigger1[CDC: session.created]
    CreateRow --> CreateWS[Create git worktree or standalone directory]
    CreateWS --> LaunchMode{Persisted mode}
    LaunchMode -->|tui| CreateRT[Launch native PTY / ConPTY / tmux]
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

### Session interface handoff

An interface switch is a controller replacement inside the existing AO session,
not a new session. The session id, optional project, workspace, lifecycle facts,
and provider-native conversation id stay the same. For project sessions, branch
and PR ownership also stay the same. Only the mode-owned controller changes.

The generic coordinator lives in `session_manager`; providers opt in through the
small `AgentInterfaceHandoff` capability only after their TUI resume id and Chat
protocol id are proven to name the same native conversation. Claude Code and
Codex currently satisfy that contract. Merely having a Chat/ACP driver is not
enough to enable switching for another harness.

The native ID handed over is the current Terminal conversation, which can differ
from the last Chat provider (for example after replacing an orchestrator). This
does not prove that the new provider inherited the old context. Session Manager
reserves a `ChatProviderHandoff` only from a matching durable TUI→Chat transition;
ordinary resumes retain the exact-handle check. Chat resumes the verified target,
reconciles only its provider scope, and prepares a visible context boundary.
Lifecycle and SQLite atomically publish that boundary, native history, controller
generation, and any project-narrative ownership transfer, checking the observed
owner, head, sequence, and controller fence again after provider I/O. Prior rows
remain intact, but are not represented as context inherited by the new provider.
Ordinary Terminal restore retains its fresh-start fallback when native history is
unavailable, including rollback and crash recovery. Prior Chat rows remain intact;
returning with a new native identity publishes a separate context boundary rather
than claiming continuity. A Terminal→Chat handoff still requires native replay and
never silently substitutes a fresh Chat provider.

The native-history barrier combines trusted native checkpoints with the
latest completed AO turn in the active provider scope. A newer completed turn can
supersede a legacy hook fact tied to an older settled turn; otherwise a Chat answer followed by an
immediate round trip would keep waiting for the older Terminal answer to be last.
Hook timestamps must prove the fact predates the superseding turn; repeated text
alone is not evidence. A hook newer than the durable completion requires settled
replay after that high-water turn. Unknown hook facts still gate replay. Hook
observation time also orders native identities within a launch, so delayed hooks
cannot replace the current identity's facts.

Independent handoff publication settles the retired predecessor's work and fails
pending requests in the same transaction as history and ownership. Codex scopes
projection IDs at the adapter boundary and decodes them for native RPCs. A durable
branch flag preserves legacy unscoped Codex IDs on upgrade; native forks inherit
that flag, while new provider boundaries use scoped IDs.
When Codex proves fork ancestry, replay omits copied prefixes only if their stable
item IDs and complete content match retained ancestor rows. Those rows stay in
their original scope. Unknown ancestry or changed content is retained in full.

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
`capture-pane`, while macOS and Windows detached PTY hosts maintain a VT cell
model beside their historical replay ring. AO accepts only repeated observations
of an idle surface with an empty composer, held across the settle window; a
visible draft fails with the source untouched and requires the user to submit,
clear, or explicitly discard it. Adapter/runtime pairs without rendered-surface
support retain the causally newer idle-fact or legacy terminal-idle fallback. An
unverified idle state has a bounded proof window; active work or a user-paced
decision remains unbounded.

### Observation flow

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

### Feedback routing flow

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

### SQLite schema

```mermaid
erDiagram
    projects o|--o{ sessions : optionally_owns
    projects o|--o| conversations : optionally_owns_orchestrator_narrative
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
    change_log }o--o| projects : optionally_tracks
    change_log }o--o| sessions : optionally_tracks
    change_log }|--|| pull_requests : tracks

    projects {
        string id PK
        string name
        string repo
        jsonb config
    }

    sessions {
        string id PK
        string project_id FK "nullable for standalone workers"
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
        string project_id FK "nullable for standalone conversations"
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

### CDC pipeline

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

## Status derivation

### Display status precedence

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

### PR pipeline states

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

## Lifecycle management

### Lifecycle manager responsibilities

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

### Session state machine

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

### Termination guardrails

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

## Observation loops

### SCM observer

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

### Runtime reaper

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

### Observation integration

```mermaid
flowchart LR
    subgraph External["External State"]
        GitHub[GitHub API]
        Runtimes[native PTY / ConPTY / tmux]
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

## HTTP layer

### API structure

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

### Multi-listener architecture (loopback + LAN)

The daemon runs two independent HTTP listeners sharing the same chi router:

1. **Primary (Loopback) Listener:** Binds `127.0.0.1:3001` with no
   authentication. All existing daemon operations, including the CLI and desktop
   app, use this listener.
2. **LAN Listener (Connect Mobile):** An opt-in second listener that binds
   `0.0.0.0:3011` (or an ephemeral fallback) **only when explicitly enabled**
   through desktop settings. Bearer-password middleware protects the app API.
   Loopback-gated shutdown, telemetry, mobile-control, and browser-control routes
   remain unavailable. Exactly `GET /api/v1/identity` is public for host and
   contract verification. Direct LAN transport is plaintext for trusted
   networks. A managed cloudflared HTTPS endpoint wraps the authenticated mobile
   path. The current v2 offer advertises Tailscale addresses as plaintext;
   Tailscale TLS setup and QR advertisement remain incomplete. The rotating password is persisted in a mode-`0600`
   file at `AO_DATA_DIR/mobile/config.json` (normally
   `~/.ao/data/mobile/config.json`); its comparison hash is in memory. See the
   [current access guide](../frontend/src/docs/content/configuration/remote-access.mdx)
   and [historical LAN ADR](adr/0001-lan-listener-for-mobile.md).

The mobile app is a second thin renderer over those same session resources. It
branches on the session's persisted `mode`: TUI attaches the existing mux PTY,
while Chat reads the paged conversation projection and uses the durable CDC SSE
stream only for targeted invalidation/reconnect. Sends, approvals, input,
provider configuration, compaction, rollback, and shell creation remain daemon
commands; no provider or lifecycle policy is implemented in React Native.

For implementation details and security model, consult `docs/adr/0001-lan-listener-for-mobile.md` and the glossary in `CONTEXT.md`.

### Request flow

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

## Terminal multiplexing

The mux is the primary agent controller only for TUI-mode sessions. Chat-mode
sessions have no agent runtime handle and never attach their provider through
tmux. They may still open session-scoped shell terminals as a worktree escape
hatch; those shells are separate resources and do not become the agent
controller.

### Terminal architecture

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
        MacPTY[macOS/Linux native PTY Host]
        ConPTY[conpty Runtime]
    end

    Browser -->|WebSocket| WS
    WS -->|attach| Mux
    Mux --> Sessions
    Sessions -->|legacy or startup fallback| TMux
    Sessions -->|create new macOS/Linux| MacPTY
    Sessions -->|create| ConPTY

    TMux -->|PTY attach| Mux
    MacPTY -->|loopback dial| Mux
    ConPTY -->|loopback dial| Mux

    Mux -->|frame| WS
    WS -->|binary| Browser

```

### Attach flow

```mermaid
sequenceDiagram
    participant Client as Browser
    participant WS as WebSocket Handler
    participant Mux as Terminal Mux
    participant Runtime as native PTY/ConPTY (legacy/fallback tmux)

    Client->>WS: WebSocket upgrade
    WS->>Mux: Attach(session, rows, cols)
    Mux->>Runtime: Attach(handle, rows, cols)

    Runtime->>Runtime: Connect to detached host or create tmux attach PTY

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
    Mux->>Runtime: Close attachment stream
```

## Browser runtime bridge

Browser automation uses a dedicated local socket (`browser.sock` on Unix,
`ao-browser[-dev]` named pipe on Windows) between the daemon and Electron. The
daemon owns command authorization/correlation; Electron owns the actual browser
targets. Commands never use the supervisor liveness socket and never enable an
unauthenticated remote-debugging port.

Electron attaches its debugger directly to the selected session's
`WebContentsView`, so the protocol transport cannot enumerate or attach to the
AO renderer or a different session. The loopback `/api/v1/browser` surface is
blocked entirely on the opt-in LAN listener.

Temporary browser profiles isolate sessions by default. Named persistent profiles can be reused across sessions, sharing their cookies/storage deliberately. Browser import reads supported source profiles without modifying them and stores results under AO data.

Request observation is an explicit, temporary browser command rather than a
standing debugger feature. Capture is off by default, bound to the active tab
that starts it, limited to 200 in-memory metadata entries, and automatically
expires within at most five minutes. AO never requests or stores request or
response bodies; it allowlists safe headers and redacts URL credentials,
fragments, and query values. Closing the tab, ending the session, or shutting
down Electron disables and discards the capture.

---

## Load-Bearing Rules

These rules are **load-bearing**. Changing them breaks fundamental
architectural assumptions:

1. **Never store display status:** Status is derived from durable facts at read time.
2. **Never treat failed probes as death:** A failed probe is a fact, not a termination signal.
3. **Never force-delete dirty worktrees:** User data safety comes before cleanup convenience.
4. **Keep all app state under `~/.ao`:** Do not use OS-default app-data locations.
5. **Keep the primary daemon on loopback:** Only the opt-in authenticated mobile path provides off-device access. Control routes remain loopback-gated.
6. **Keep the CLI thin:** All logic lives in the daemon. The CLI is an HTTP client.
7. **Use CDC as the source of truth for events:** DB triggers write to `change_log`, and the poller fans out changes.
8. **Keep adapters as leaves:** Adapters never import core packages, only ports and domain.
9. **Keep hooks gitignored:** Every file an adapter writes must be in `.gitignore`.
10. **Never change migrations:** Add new migrations instead of modifying existing ones.

---

## Summary

Agent Orchestrator's architecture is designed around:

- **Separation of concerns:** Observation, persistence, and display are distinct layers.
- **Port-based design:** Core code depends on interfaces, not implementations.
- **Durable minimalism:** Store only facts, and compute everything else.
- **Event-driven updates:** CDC broadcasts changes to all subscribers.
- **Isolation:** Each project session owns a Git worktree, each standalone worker owns an AO-managed directory, and every session has exactly one live mode-specific controller, including across handoffs.
- **Safety:** Termination is conservative, paths are validated, and hooks are gitignored.

This architecture enables parallel AI agents to work safely while maintaining complete visibility and control.
