# Desktop responsiveness program

**Status:** in progress. The measured chat acknowledgement and file-search spam
fixes below are implemented; desktop trace follow-up remains pending.

AO is a developer tool, so the product should acknowledge input immediately and
stay responsive while agents stream, terminals run, and the user resizes the
workspace. This plan addresses the reported delay from sending a chat message
to seeing it in the timeline, lag while resizing the inspector rail, and other
high-impact responsiveness risks that may share the same renderer or daemon
paths.

This document distinguishes implemented, measured changes from planned work.
The existing [chat responsiveness record](chat-responsiveness/README.md)
documents a prior measured pass over CDC delivery, streamed-text display,
scrolling, and large code highlighting.

## Execution tracker / remaining to-do

| Work item | Current status | Closure evidence still required |
| --- | --- | --- |
| Make a submitted chat message visible without waiting for the daemon | Implemented and fixture-measured | Live Electron send/stream trace with submit-to-row and first-stream-text spans |
| Prevent repeated file-tree hydration while a filter is being edited | Implemented and regression-tested | Large/deep real-worktree filter trace |
| Remove palette whole-document modal locking | Implemented; synthetic-shell open task reduced from 180.5 ms to 22.9 ms median | Matching packaged-Electron palette trace |
| Make Settings acknowledge opening before its heavy body mounts | Implemented; fixture first paint reduced from 31.6 ms to 17.3 ms median | Matching packaged-Electron Settings trace |
| Keep rail toggles from rerendering a long chat timeline | Implemented and fixture-measured | Live inspector trace in Summary, Browser, and terminal modes |
| Reduce terminal/agent switch critical-path work | Focus deferral, retained-terminal parking, and xterm resize coalescing are implemented | Latest live trace regressed from the earlier 91.3 ms median to 128.5 ms; isolate the remaining Motion/layout path before claiming closure |
| Prevent sidebar reorder completion from reattaching nested DnD/Motion synchronously | Implemented and regression-tested | Matching live sidebar-drop trace |
| Keep terminal-search bursts to one buffer scan per frame | Implemented and regression-tested | Deep-scrollback live trace |
| Verify `@`, `/`, ordinary typing, and long streamed chats under real Electron load | Isolation fixtures pass | Streaming-chat live trace; this is the remaining highest-risk chat verification |
| Keep direct inspector resize visually coupled to the pointer | Local live reflow implemented; browser fixture p95 is 2.65 ms right / 2.07 ms left | Fresh packaged-Electron drag traces for both sidebars |

## Goals

- A submitted chat message is visible as the user's action completes, rather
  than only after a daemon round trip and a full conversation refresh.
- Inspector and sidebar dragging follows the pointer without long frames or a
  backlog of terminal/native-browser resize work.
- Long conversations, active streams, and hidden surfaces do not make routine
  interactions progressively slower.
- Performance changes are measured against repeatable desktop workflows and
  protected by behavioral regression tests.

## Guardrails

- Preserve daemon authority, durable conversation ordering, idempotent sends,
  approval semantics, and the existing Chat-to-Terminal safety boundary.
- Do not replace complete conversation snapshots with an unsafe partial-update
  shortcut. Conversation CDC currently identifies a conversation, not the
  exact item or page that changed; the prior performance work explains why
  incremental history needs an explicit change-scope contract.
- Do not turn a visual delay into a hidden queue. Optimistic presentation must
  reconcile with a durable turn, surface rejection, and never duplicate a
  message.
- Optimize real user paths first. Microbenchmarks guide investigation but do
  not substitute for packaged Electron traces with an isolated daemon.

## Initial code-path observations

These observations guide the first measurements; they are not yet a root-cause
finding from a production trace.

### Message send to visible timeline item

`useConversationCommands` writes a pending-send marker to its dispatch-tracking
cache in `onMutate`. It does not add a user message to the conversation snapshot.
After a successful `POST /conversation/messages`, it records the accepted turn
and starts a background conversation invalidation. The visible timeline therefore
depends on a later authoritative snapshot to contain the message.

Conversation CDC also deliberately batches invalidations in a 150 ms window.
That batching protects the renderer during a stream, but a user-visible send
must not wait for it. The direct post-send invalidation is separate from the
CDC path, so the measurements need to distinguish HTTP acceptance, snapshot
fetch, CDC delivery, and React commit rather than attributing every delay to the
same debounce.

### Inspector and sidebar resizing

`useResizable` already coalesces pointer moves to one CSS custom-property write
per animation frame. The inspector consumes that property in both its layout
gap and its Motion-managed rail. When Browser is active, `useBrowserView`
observes the layout and sends native BrowserView bounds through Electron IPC;
when a terminal is active, resize observation can also trigger terminal fitting.

The likely cost is therefore the combined path, not necessarily the pointer
handler itself. Measurements must compare summary/files, Browser, and terminal
inspector modes before changing scheduling or fidelity.

## Development-trace validation (2026-09-09)

Nine renderer Performance-panel traces were captured against the Vite/Electron
development renderer (`localhost:5173`). They are strong evidence of the
affected code paths, but not release-build timings: React development mode and
Strict Mode add work which must be excluded from the final packaged-app
comparison.

An input handler or renderer task above 50 ms misses at least three 60 Hz
frames. The measured results are:

| Recorded interaction | Verdict | Evidence from renderer trace |
| --- | --- | --- |
| Toggle between terminals and agents | Improved, still above budget | Matching development trace after the fixes: 13 clicks, 91 ms median, 115 ms maximum, down from 168 ms and 248 ms. Layout remains the primary residual cost. |
| Toggle Settings modal | Confirmed | 4 keyboard handlers reached 92.5 ms. |
| Toggle command palette with mouse | Confirmed severe | 3 open interactions reached 136 ms on pointer-down and 223 ms on click. |
| Drag projects in left sidebar | Confirmed at drop | Pointer movement is cheap; the two pointer-up handlers took 312 and 355 ms. `ProjectItem` work is visible in samples. |
| Spam the Chat UI | Confirmed, highest priority | 47 renderer tasks exceeded 50 ms, maximum 478 ms. Typing reached 69 ms; samples include `ChatWorkspace` and repeated console/stream work. |
| Terminal resize | Not reproduced as a blocked drag | 294 pointer moves were at most 0.3 ms. Layout/style reached 40 ms and two unrelated long tasks remain, so this needs monitoring but is not the first resize fix. |
| Toggle right sidebar | Confirmed severe | All 11 clicks exceeded 50 ms: 125 ms median, 206 ms maximum. The trace shows broad React commit and paint/compositor activity around `SessionView`. |
| Browser resize | Not reproduced as a blocked drag | 116 pointer moves were at most 0.1 ms; the worst style/layout event was 32 ms. Native bounds work remains a measured-candidate, not a proven cause. |
| Chat send/stream | Confirmed renderer stalls, incomplete end-to-end span | 12 renderer tasks exceeded 50 ms, maximum 522 ms. The trace lacks explicit submit, acceptance, and first-row-paint markers, so it cannot apportion daemon versus renderer delay. |

The recurring pattern in the confirmed cases is expensive React commit and
passive-effect work rather than a single slow Layout event. Closed global
surfaces also appear in samples across unrelated interactions. In particular,
the shell always mounts the keyboard-shortcuts settings dialog, and the command
palette remains mounted to support its close animation and shortcut. These are
audit candidates for memo boundaries or lazy mounting; they are not yet
established as the dominant cost of every interaction.

## Spam-resilience audit (2026-09-09)

The reported failure mode is not limited to a particular button: it is what
happens when a user produces input faster than a surface can render, filter,
fetch, or transition. This audit inventories the renderer's text entry,
completion, selection, and repeated-action paths. A static risk is not treated
as a confirmed performance failure until it has a real renderer measurement or
a supplied trace.

### Measured autocomplete boundary

The `@` path list is deliberately local so every character does not start a
daemon round trip. The daemon nevertheless permits up to 5,000 workspace
paths. `rankFiles` currently scans and scores every supplied path, sorts its
matches, and renders at most 50 choices on every completion update.

An opt-in Playwright workload now exercises that real Lexical composer with
5,000 synthetic paths and browser-generated keyboard input. Five isolated
Chromium runs produced 65 `beforeinput` samples:

| Metric | Result |
| --- | --- |
| Candidate paths / rendered choices | 5,000 / 50 |
| Input handler p50 / p95 / maximum | 0.1 ms / 16.5 ms / 22.6 ms |
| Maximum sampled animation-frame gap | 16.8 ms |
| Long Tasks | One run reported one; the remaining four reported none |

This does **not** reproduce a persistent dropped-frame failure at the server's
current maximum. It rules out file completion alone as the primary explanation
for the 478 ms chat trace. The one 22.6 ms sample means the flow needs a
regression guard, and it should be retested in a packaged Electron window with
a real large worktree.

The benchmark is intentionally opt-in, uses a production renderer rather than
the running desktop app, and writes measurements only when `AO_PERF_BENCH=1`.
It lives in `frontend/e2e/performance/harness.tsx` and
`frontend/e2e/chat-performance.spec.ts`.

### Measured `/` completion boundary

The same real-Lexical workload now covers 5,000 synthetic skills, including
display names and descriptions, and types `/chatcomposer` through browser
keyboard events. Three isolated Chromium runs produced 39 `beforeinput`
samples:

| Metric | Result |
| --- | --- |
| Candidate skills / rendered choices | 5,000 / 50 |
| Input handler p50 / p95 / maximum | 0 ms / 12.7 ms / 17.8 ms |
| Maximum sampled animation-frame gap | 16.7 ms |
| Long Tasks | One per run during initial work; none was paired with a frame gap over one frame |

The skill matcher is therefore also not a reproduced sustained typing problem
at a deliberately extreme catalog size. The long-task samples keep it under
regression coverage, but the supplied chat trace still points more strongly to
the surrounding streaming timeline and global React work.

### Measured command-palette data boundary

The command-palette harness constructs 100 projects with 5,000 worker sessions
and 1,000 open PRs, yielding 9,106 command items once session and PR actions
are included. Five isolated Chromium runs reported:

| Metric | Result |
| --- | --- |
| Command construction p50 / maximum | 11.5 ms / 13.2 ms |
| Query filtering p50 / maximum | 3.4 ms / 4.9 ms |
| Rendered result cap | 20 |

The workload covers broad text, project, PR-number, PR-action, and no-match
queries. This rules out command construction/ranking as the explanation for
the supplied 223 ms palette-open trace at a much larger data size than a
normal workspace.

An interaction-level Chrome trace then confirmed that remaining cause. With a
synthetic but real shell containing 400 worker sessions (11,146 DOM elements),
the previous Radix modal path had a three-run median **180.5 ms** keydown task,
**88.5 ms** largest `UpdateLayoutTree`, and **242.3 ms** first palette paint.
The command construction work is not the cause: Radix's modal scroll lock and
background isolation forced whole-shell layout/style work.

The palette now uses its own full-screen overlay with Radix's non-modal dialog
primitive, an explicit `aria-modal`, and a trapped looping focus scope. The
overlay owns outside pointer and wheel input, while the focus scope preserves
keyboard containment. In three equivalent runs the median keydown task was
**22.9 ms**, largest layout update **1.9 ms**, and first palette paint
**65.3 ms**. A component test verifies the dialog remains semantically modal,
renders the overlay, and keeps repeated Tab navigation inside it. This is a
large synthetic-shell improvement, but it still needs a comparable packaged
Electron trace before we claim the same delta for the supplied 223 ms trace.

### Measured Settings dialog mount boundary

The supplied development-Electron Settings trace had keyboard-open handlers up
to 92.5 ms. A new opt-in browser workload isolates the already-warm General
settings dialog, including the real Radix dialog and settings form. Before the
change, the General form added **10.5–19.3 ms** of React work to the opening
commit (five runs, 12.3 ms median maximum commit) and first dialog paint was
**27.5–62.8 ms** (31.6 ms median). That is enough to explain an avoidable
missed frame, though it cannot alone attribute the 92.5 ms desktop outlier.

The dialog now mounts its focus/dismissal chrome synchronously and mounts the
chosen project or global form in the following animation frame. Five equivalent
runs measured a **17.3 ms** median initial dialog paint and **7.6 ms** median
maximum commit; the populated body first painted at **30.1 ms** median. The
dialog therefore acknowledges the shortcut or click promptly while preserving
the full form on the next frame. The performance workload and a component test
protect this scheduling boundary. This is browser-renderer evidence only;
retest the dialog in packaged Electron before crediting the full 92.5 ms trace
with this change.

### Implemented acknowledgement path

The renderer now keeps an ephemeral local echo separately from the authoritative
conversation snapshot. A plain-text send clears its composer immediately, adds
one local human row during the request, associates that row with the returned
turn id, and removes it only when the exact durable human message arrives. A
rejection removes the echo and restores the draft. This avoids fabricating a
partial server snapshot while making acknowledgement independent of the
conversation invalidation window.

An opt-in Playwright/Chromium workload deliberately holds the send request for
600 ms and uses a real button click, React Query mutation, and `ChatWorkspace`.
Across five runs, the local row committed in **8.9–11.4 ms** and was observed in
the following animation frame in **28.2–29.3 ms**. The request was still pending
at that paint in every run. These are synthetic renderer timings, not packaged
Electron results, but they prove the acknowledgement no longer waits for daemon
acceptance or a snapshot refresh.

### Measured CDC first-update latency (leading-edge flush)

The CDC invalidation window was a fixed 150 ms trailing batch measured from the
first event: every update after a quiet period — the first streamed token of a
new turn, a status change, a PR fact — waited out the full window before the
renderer refreshed. The window now flushes on the leading edge when the last
flush is at least a window old, and still coalesces a burst into one trailing
flush otherwise. `invalidate()` already deduplicates the resulting refetches per
key (one in-flight plus one queued), so the leading edge cannot start a refetch
storm.

The opt-in `events` renderer workload emits a conversation event every 100 ms
for 2 s and records each cache flush. First refresh dropped from **152 ms** to
**3.2 ms**; flush count over the burst rose from 10 to 14 (the added leading
flush plus the same per-window trailing cadence), and each session is still
deduplicated within its window. Behavioral coverage in
`event-transport.test.ts` pins the immediate leading flush, the continuous
per-window cadence, and the within-window dedup. This is synthetic renderer
evidence; the change affects every CDC-driven surface, so a live streaming
trace remains the packaged-Electron confirmation.

### Implemented rail and terminal interaction boundaries

The supplied inspector-toggle trace shows the click callback itself taking about
1 ms, followed by a 175 ms React microtask/commit and two layout passes. The
inspector rail now memoizes its full content surface and only passes Browser and
Files payloads while those tabs are active. A close animation therefore does
not need to recompute the Summary subtree merely because its rail state changed.

The conversation minimap previously subscribed the whole `Timeline` to inspector
open state solely to hide its narrow scrollbar control. That made every inspector
toggle recommit a potentially long conversation history. The timeline now keeps
that state at the minimap boundary: a direct store subscription updates the
track's visibility, focusability, and pointer interaction without rerendering
the timeline. A React Profiler regression test with 250 history items confirms
an inspector toggle causes no `ChatWorkspace` commit; an interaction test
confirms the hidden minimap cannot scroll the history and becomes accessible
again once the inspector closes. This removes one concrete React amplification,
but has no matching long-live-chat desktop trace yet, so no timing improvement
is claimed.

An opt-in Chrome interaction workload now also mounts that complete 250-turn
timeline (10,115 DOM elements) and opens the inspector through a real click.
Across three runs the click task was **0.203 ms** median (0.241 ms maximum),
the largest `UpdateLayoutTree` was **1.75 ms** across 253 elements, and the
first following animation frame was **79.4 ms** median. This is meaningful
evidence that the timeline itself no longer amplifies the toggle into a
whole-history style/layout pass. It remains a browser fixture, not evidence
about the native inspector rail animation, BrowserView, or a live stream.

A current repeat on the working tree (2026-09-10) kept that result stable.
The ordinary inspector fixture (470 DOM elements) recorded a **5.48 ms**
median click task and **1.21 ms** largest layout update. The 250-turn fixture
(10,115 DOM elements) recorded a **0.18 ms** median click task, **1.53 ms**
largest layout update over 253 elements, and **69.4 ms** median first following
animation frame. The ordinary fixture's close animation did not become hidden
until roughly 865 ms in headless Chromium; that is an animation-settle
observation, not a blocked input task and not a native-desktop measurement.
The next live trace must distinguish terminal fitting and BrowserView bounds
updates during a real drag before changing the rail's product behavior.

The parent shell needed a separate boundary: `SessionView` must rerender to
drive the inspector rail, but it was recreating chat-surface action elements on
every rail update. `SessionChatSurface` is now memoized, and `SessionView`
keeps the chrome, terminal action, and session action elements referentially
stable when their content has not changed. A shell regression test wraps the
chat-surface boundary and verifies that an inspector toggle does not rerender
it. This is deliberately a behavioral guard rather than a claimed timing delta;
the next long live-chat trace must measure the combined rail work.

The terminal/agent-switch trace identifies retained-terminal parking and xterm
focus as participating work. Focus for an already-permitted terminal handoff is
now scheduled in a subsequent task rather than synchronously inside the
discrete React update; blocked focus keeps the existing animation-frame retry
for a closing dialog. This preserves terminal accessibility while removing that
browser focus work from the click’s critical section. These two changes require
a comparable packaged Electron trace before claiming an interaction-time delta.

Raw analysis of the supplied `toggle-between-terminals-and-agents.json` makes
the baseline more specific. Its nine click tasks have a **168.2 ms** median and
**247.9 ms** maximum. Each switch is dominated by a React microtask/commit and
Motion measurement rather than its click callback: sampled time includes
**31–66 ms** in Motion `measure`/`run`, **27–35 ms** in geometry reads or
attribute updates, and **18–26 ms** in browser focus. The click also triggers
two or three `UpdateLayoutTree` passes of **22–28 ms** across roughly
**1,165–1,425 elements**. A separate 424 ms follow-on microtask samples xterm
and layout reads (`offsetWidth`, `scrollWidth`, `scrollLeft`) rather than a
single application function. This validates the focus deferral, retained
terminal isolation, and broad-shell render reductions already implemented; it
also shows that the current trace predates the rebuilt production package and
must be recaptured before further Motion or xterm scheduling changes.

### Terminal/agent switch comparison (development renderer)

`new-switching.json` is a matching follow-up trace from the same Vite/Electron
development environment, recorded after the terminal-focus and shell-render
changes. This is a valid before/after development comparison, not a claim about
the packaged release build.

| Metric | Before | After | Change |
| --- | ---: | ---: | ---: |
| Click tasks | 9 | 13 | More repeated samples |
| Click p50 | 168.2 ms | 91.3 ms | -45.7% |
| Click p95 | 227.6 ms | 115.4 ms | -49.3% |
| Click maximum | 247.9 ms | 115.5 ms | -53.4% |

The former focus sample is absent from the dominant after-click work, which is
consistent with scheduling focus outside the discrete update. The remaining
clicks alternate between three layout passes totaling about **40 ms** over
**1,047 elements** and four passes totaling about **65 ms** over **1,271
elements**. Individual `UpdateLayoutTree` passes remain **19–25 ms**, so every
switch can still miss frames despite the large improvement. The next narrow
investigation should reduce retained-terminal/xterm layout scope, rather than
add generic input throttling or remove Motion blindly.

The next source-app trace, `new-new-switching.json`, is a valid follow-up: its
renderer process is the current checkout's Electron process and its DevTools
metadata reports no CPU throttling. It reports **17** switch clicks at
**128.5 ms p50**, **150.1 ms p95**, and **150.1 ms maximum**. That is still
better than the original 168.2 ms median, but it regresses from the 91.3 ms
intermediate capture, so the parking/fit changes are not credited with a
switching improvement. Its click tasks repeatedly include Motion frame work
and two broad layout passes of about **20 ms** over 1,029 elements and **24 ms**
over 1,254 elements. The next change must target that shared shell/Motion
layout path, not the fast click handler itself.

### Diagnosed next candidate: contain the center/terminal pane (needs trace)

The two broad layout passes over ~1,029 and ~1,254 elements on each switch are
a whole-shell relayout: swapping the visible terminal (or the agent/reviewer
pane) changes a large subtree, and because the center/terminal pane is not a
layout-containment boundary, the browser recomputes the shell's layout tree
outward rather than only the pane. This is the same failure the conversation
timeline already fixed with `contain: layout paint`, which scoped a composer
update to the timeline instead of the whole shell.

The specific candidate is therefore to make the center-pane box a
`contain: layout` boundary (`CenterPane` root / the central session column in
`SessionView`) so a terminal/agent switch cannot invalidate the surrounding
shell layout. Use `layout` alone, not `paint`, so escaping tooltips/menus are
not clipped (Radix already portals to `body`, but the tab rename input and
scroll fades live inside the pane). The pane is already a fixed flex child
(`flex-1 min-h-0 min-w-0`), so its own size is set by flex, not content, which
is the precondition for `contain: layout` to be visually inert.

This is a source-level diagnosis, not an applied change: `contain` can only be
credited after a packaged-Electron switch trace confirms the broad layout pass
shrinks and a visual pass confirms no clipped/again-escaping surface. It must
not be shipped on faith, because the whole-shell relayout it targets is only
reproducible in the packaged app.

### Sidebar reorder churn benchmark and memory audit (2026-09-10)

A reported "slight lag when I start dragging and when I drop" a project row led
to two checks.

**Reorder churn.** The sidebar neutralizes every expanded project's nested
session sortables during a project drag by unmounting the `DndContext`/
`SortableContext` and mounting plain rows (`Sidebar.tsx`, the
`projectDragInProgress` swap), then remounting on drop. A hypothesis was that
keeping the tree mounted and toggling dnd-kit's `disabled` would avoid that
unmount/remount cost. An opt-in `projectDragChurn` renderer workload
(`harness.tsx`) mounts 8 projects x 12 sortable rows with real dnd-kit and
measures the start and drop commit for both strategies. Median of three
toggles: swap **start 1.7 ms / drop 3.9 ms**; keep-mounted-disabled
**start 4.0 ms / drop 3.8 ms**. Keeping the tree mounted is *slower* on start
because a disabled `useSortable` still runs its hook, while plain rows do not.
The current swap is therefore the correct primitive and must not be replaced;
the residual desktop lag is the raw cost of re-rendering the real (heavy)
session rows and the `DragOverlay` preview across the whole sidebar, which is
only reproducible in a packaged-Electron reorder trace. Sidebar leaf rows
(`SortableSessionRow`, `PinnedSessionRow`), `ProjectItem`, and
`ProjectDragPreview` are already `memo`-wrapped, and `SessionView` is not
remounted on a session switch, so the cheap structural wins are already taken.

**Memory audit.** A sweep of the long-lived renderer surfaces (terminal mux,
xterm, event transport, workspace-file events, diff worker, telemetry, browser
view, notification runtime) found no leaked listeners, observers, timers, or
workers: EventSource/WebSocket handlers are torn down with `close()`, observers
use one `disconnect()` per lifecycle, the diff-parser worker is a deliberate
app-lifetime singleton with a drained pending-request map, and the only
never-removed listeners are telemetry's intentional app-lifetime `error`/
`unhandledrejection` crash handlers. No memory fix was required.

### Traced project-reorder drop cost and fix (2026-09-10)

A supplied packaged-dev reorder trace (`sidebar-reordear.json`) settled the
question the churn benchmark could not: the drag **start** is cheap
(`pointerdown`/`pointermove` under 1 ms), and essentially all the lag is at the
**drop** — three `pointerup` dispatches of **324, 368, and 402 ms**. Inside the
worst `pointerup`, `UpdateLayoutTree` was only 61 ms; the dominant cost was a
long run of `(native) measure` calls (each ~7 ms, ~138 ms total) plus a matching
`run`/commit band — i.e. repeated forced synchronous layout measurement, not
dnd-kit's reorder and not the swap primitive. (The trace is a dev build, so
React StrictMode double-invocation and dev-mode overhead inflate the absolute
numbers roughly 2x; the packaged app is faster, but the shape holds.)

Root cause: every expanded project's session list is a
`motion.div animate={{ height: "auto" }}`, and `height:"auto"` forces Motion to
re-measure natural height whenever it re-renders. On drop, `onProjectDragEnd`
changes `draggingProjectId` (to null) and `projectDropSettling` (to true) in one
commit, and both were passed as raw props to **every** `ProjectItemContent`, so
every expanded project re-rendered and re-measured — even though the value they
actually consume, `projectDragInProgress`, was unchanged (`true` during the
settle window).

Fix (`Sidebar.tsx`): pass the two *derived booleans* the content actually needs
— `projectDragActive` (any drag/settle active) and `isDragged` (this project is
the dragged one) — instead of the raw id and settling flag. At the drop commit
`projectDragActive` stays `true` and `isDragged` stays `false` for every
non-dragged project, so their memoized content skips the re-render and its
`height:"auto"` re-measurement entirely; only the one dragged project re-renders
(to drop its dragging style). The reorder still moves the memoized rows in the
DOM via React list reconciliation. Behavior is unchanged (99 Sidebar tests pass,
typecheck clean); the drop measurement now scales with 1 project instead of
every expanded project. A fresh packaged trace should confirm the `pointerup`
span shrinks.

### Measurement-free session-list expand (grid-template-rows)

The trace's dominant drop cost was a long run of forced synchronous `(native)
measure` calls. The profile carried no call stacks, so Motion's `height:"auto"`
animation on every expanded project's session list could not be *proven* the
source, but it is the one construct on that path that forces a layout read on
each render. The session list now animates `grid-template-rows: 0fr -> 1fr`
(with an inner `min-height:0; overflow:hidden` clipper) instead of
`height: 0 -> auto`. Grid-track interpolation needs no measurement: a browser
probe confirmed Motion 12.43 smoothly interpolates the computed track
(2.6 -> 9.7 -> 21 -> 37 -> 57 -> 86 -> 122 -> 158 px toward the natural height),
so the enter/exit animation is preserved without a `getBoundingClientRect`.

This removes the measurement from *every* session-list render, not just the
drag: the normal disclosure expand/collapse is now measurement-free too. Enter,
exit, and the inner y/opacity fade are unchanged; 99 Sidebar tests pass and
typecheck is clean. The animation correctness is verified (probe + tests); the
drag/expand timing improvement is inferred from the trace and should be
confirmed with a fresh packaged reorder trace, ideally one recorded with JS
call stacks so the remaining `measure` attribution is exact.

### Direct inspector-resize diagnosis (development renderer)

`drag-right.json` confirms that direct rail resize was genuinely visually
behind the pointer. The 343 `pointermove` handlers themselves were healthy
(**0.05 ms median**, **0.12 ms p95**, **0.24 ms maximum**), but nearly every
drag frame immediately incurred a **28–30 ms** `UpdateLayoutTree` over about
**1,414 elements**. The main thread therefore had only enough capacity for
roughly 30 visual updates per second before any other renderer work; adding
pointer throttling would not correct this.

The first attempted fix previewed only the absolute rail and deferred the flex
gap reflow until pointer release. Although its fixture layout numbers improved,
it broke the expected direct-manipulation behavior and still felt delayed in the
desktop app, so that approach was rejected. The replacement keeps the panel and
content reflow synchronized on every animation frame while moving the live width
custom property off `:root` and onto only the elements that consume it. This
targets the trace-confirmed broad style invalidation without weakening visual
coupling. In three frame-paced browser runs, the right sidebar affected at most
**241 elements** with **1.29 ms median / 2.65 ms p95** meaningful layouts; the
left affected at most **130 elements** with **1.42 ms median / 2.07 ms p95**.
Both sidebars need fresh packaged-Electron traces before closure.

### Long-history typing boundary (browser fixture)

The original chat-spam trace showed Lexical selection and composer updates
repeatedly forcing 22–30 ms layouts across roughly 1,180 elements. The timeline
is now a `contain: layout paint` boundary: it is a fixed-size flex item with its
own scroll viewport, so a composer update cannot invalidate the surrounding
shell or the complete mounted history.

An opt-in Playwright workload mounts 250 real conversation turns (9,610 DOM
elements) and enters 33 characters through the real Lexical editor. Across
three runs, its 99 `beforeinput` samples measured **0.2 ms p95** and **9.7 ms
maximum**. The largest frame gap was **41.7 ms** and no long task began after
measurement started. The latter detail matters: the harness now starts its
long-task observer after mounting, so the one-time 250-turn mount is not
misattributed to typing. This is browser-fixture evidence only; a live
streaming Electron trace is still required before declaring the original chat
issue resolved.

Terminal search was also a concrete burst-input risk: every character called
the xterm search addon's whole-buffer incremental scan synchronously. It now
coalesces rapid changes to the newest query once per animation frame. A
regression test drives three changes before a frame and asserts exactly one
scan, for the final query. This does not prove the cost of an individual deep
scrollback scan, so that remains a packaged-app trace requirement.

The inspector animation had a separate terminal-resize amplification path:
while its width changed, xterm called `FitAddon.fit()` synchronously from every
`ResizeObserver` delivery. That combines a geometry read and possible renderer
allocation with Chromium's in-progress layout. The live-resize path now queues
at most one fit for the following animation frame and preserves the existing
quiet-window/final exact fit. A unit test triggers two observer deliveries in
one frame and verifies that they create no synchronous fit and exactly one
deferred fit. This is a source-backed scheduling fix; it requires the same
live terminal/rail trace as the parking change before any timing reduction is
claimed.

### Interaction inventory and current verdict

| Surface / spam action | Current behavior | Verdict and next evidence |
| --- | --- | --- |
| Chat ordinary typing | Lexical owns the text; the composer has a unit-level React Profiler assertion that typing after the first character does not recommit the surrounding composer. The timeline is layout/paint-contained. | Healthy through a 250-turn browser workload. Re-test while a real long conversation streams, because the existing supplied trace confirms broad `ChatWorkspace` stalls. |
| `@` file completion | Local 5,000-item scan, sort, and 50-row render per completion update. | Measured above: not a persistent stall in isolation. Keep as a guarded boundary, not first fix. |
| `/` skill completion | Local scan, score, sort, and 50-row render. `rankSkills` has the same explicit 50-result cap as file completion. | Lower risk by design. Measure only with an unusually large real skill catalog. |
| Command palette query | While open it builds command items from workspaces and sorts all matches in JavaScript; DOM output is capped to 20 results. While closed it avoids the live workspace subscription. | Measured with 9,106 items: construction ≤13.2 ms and each tested query ≤4.9 ms. Open/close remains trace-confirmed severe and needs a packaged comparison. |
| Model selector query | Builds a search index only when its catalog changes, uses trigram postings for normal queries, and renders at most 50 models. | Lower risk by design. Include in a broad settings trace, not a first fix. |
| Generic settings / shortcut filters | Small in-memory list filters; keyboard-shortcut settings are mounted from the shell even when closed. | The filters are low-risk; closed-surface mount/subscription cost remains a global-render audit candidate. |
| File explorer filter | `react-arborist` virtualizes rendered rows. A non-empty filter still hydrates the tree recursively to make nested search complete, but rapid filter changes now share one per-session hydration rather than restarting it. | Fixed repeated-input amplification and covered by a test asserting one root/nested request sequence across `t` → `ta` → `target`. One initial deep-tree scan is still high-risk and needs a real large-worktree trace. |
| Terminal search | Every query change calls xterm's incremental `findNext`, which searches the terminal buffer and updates decorations. | High-risk code path for a terminal with substantial scrollback. Needs a real xterm trace because a DOM-only harness cannot reproduce the buffer/search-addon cost. |
| Browser URL/history suggestions | History lookup is debounced by 120 ms before IPC. | Designed to resist spam. Verify stale-result handling and IPC count in one browser-address-bar trace. |
| Sidebar drag/reorder | Pointer movement was cheap in the supplied trace; pointer-up took 312–355 ms. | Confirmed expensive completion, not drag-frame spam. Profile persisted reorder and `ProjectItem` commit work. |
| Inspector tabs / terminal-session switches / settings dialogs | Repeated opens and switches trigger broad React commit/paint work. | Confirmed by supplied traces; these remain above unmeasured input filters in implementation priority. |
| Browser and terminal resizing | Pointer-move handlers did not block in supplied traces. | Not a first drag-throttling target. Preserve monitoring while investigating broad commits and native-view work. |

The palette previously retained the complete `CreateProjectFlow` beside its
always-mounted shortcut surface. That flow has cloud/query hooks and several
dialog branches, while its only palette entry point is the one-shot **New
project** action. It now mounts on that action only and receives an explicit
post-mount open signal. A regression test verifies that normal palette opening
does not mount the import flow and that selecting **New project** still opens
the picker. This reduces idle and query-spam work; its first-open cost needs a
packaged trace like the other palette changes.

The project-sidebar trace showed its cost at pointer-up, not while dragging.
The sidebar already isolated per-move DnD updates, but a completed project
drop re-enabled every nested session DnD context and Motion position layout in
the same event. The new drop-settling boundary commits the reordered project
list immediately, then restores those nested contexts in a subsequent task. A
regression test verifies both the immediate order and the deferred reattach.

### Repeated-interaction coverage map

The audit is intentionally grouped by repeated-input family rather than every
one-shot confirmation button. These are the renderer paths where a user can
outpace work, based on the structural scan of renderer components and hooks:

| Family | Current protection or observed behavior | Verdict |
| --- | --- | --- |
| Chat ordinary text, `/`, `@`, attachments | Lexical keeps ordinary draft edits local; both completion lists cap rendering at 50. `@` and `/` have 5,000-candidate browser workloads. Attachment staging starts only from an explicit pick/drop action. | Completion paths are bounded in isolation; reproduce typing during an active long stream. |
| Chat send, queue, steer, interrupt | Send has renderer-local acknowledgement; mutations block duplicate execution. | Visible send acknowledgement is fixed and measured synthetically; durable/streaming work remains the primary live-chat trace target. |
| Terminal search and terminal input | Xterm owns terminal writes; search now coalesces rapid query edits to one latest whole-buffer scan per frame. | Burst amplification fixed. Capture deep scrollback to find the remaining cost per scan. |
| File tree and changed-file filter | Virtual rows; changed-only tree is memoized. A complete first nested search hydrates lazily, but repeated filter text shares that one hydration. | Character-by-character repeated fetch amplification fixed; first deep scan is still unmeasured. |
| Command palette open and search | Result DOM is limited to 20; workspace subscription is off while closed; per-session review queries start after initial open paint. Its overlay now retains focus/pointer containment without Radix's whole-document modal lock. | Open has a measured synthetic-shell fix. Re-capture the supplied interaction in packaged Electron. |
| Model picker search | Model catalog gets a memoized trigram/provider index and renders at most 50 candidates. | Structurally bounded; include in a settings trace with a large catalog before changing it. |
| Browser address/history and browser tabs | History IPC has a 120 ms trailing debounce and cancels stale effects. Browser tab reorder is a separate drag/drop path. | Address typing is protected; tab-drag completion needs the same pointer-up profiling as project reorder. |
| Sidebar project reorder and tab/session switches | Reorder movement is cheap but drop is trace-confirmed expensive; terminal/session switching has confirmed parking/focus work. | High-priority post-change packaged traces. |
| Rail, pane, and browser resizing | Pointer moves are animation-frame coalesced and traces did not show a blocked move handler. | Do not add throttles blindly; measure broad React/native-view work by active inspector tab. |
| Forms, settings filters, menus, and editable metadata | These are controlled local inputs or small in-memory option lists; the settings shell is currently mounted globally and settings open was trace-confirmed slow. | Individual typing is lower risk. Audit closed-surface subscriptions and mount cost through the existing settings trace. |
| Timeline expand/collapse, diff/file selection, notifications, and review controls | These are selection/click driven, not per-character network work; review controls now defer their secondary queries until palette first paint. | Inspect if a trace attributes a long commit to one of these surfaces; no generic throttling justified. |

The static sweep also covered New Task/task metadata fields, inline project and
browser-profile rename controls, Browser URL/history input, import destination
names, report text, and per-turn configuration controls. They either retain a
local draft until explicit commit, cap/debounce their remote lookup, or are
bounded option menus. They remain covered by the family-level trace protocol,
but there is no code evidence that supports adding input throttles to them now.

### Targeted manual captures still required

The following cannot be measured faithfully from the isolated renderer because
their cost depends on real daemon data, BrowserView IPC, or xterm state. Record
each in a packaged build with the Performance panel, including at least 5
seconds of idle time before and after the action:

1. In a large/deep worktree, open Files and type, erase, then rapidly replace a
   filter three times. Keep the Network lane visible to verify one recursive
   hydration per session and measure the remaining first-search frame gap.
2. With a terminal containing substantial scrollback, open terminal search and
   type a common term rapidly, toggle regex, then clear it. Include Main,
   Event Log, and the xterm-related call stack.
3. Populate many projects/sessions, open the command palette, and type and
   erase several queries rapidly. Capture event durations, React commits, and
   visible result count.
4. With a streaming long chat, type ordinary prose, `/`, and `@` completions.
   Include Console only if it is already known to be noisy so the stream-log
   sample from the existing trace can be identified rather than guessed at.

## Work plan

### 1. Establish a desktop interaction baseline

Create a repeatable, isolated performance harness covering normal and stressed
states: a fresh conversation, an active stream, a long conversation, Browser
open, and a terminal open. Capture Chrome/Electron traces alongside structured
timing records.

Record these spans separately:

| Interaction | Required timestamps / counters |
| --- | --- |
| Send a message | submit event; local row commit; HTTP request start/end; daemon acceptance; durable write; CDC received; snapshot response; first provider text; first provider text committed |
| Resize a rail | pointer-move arrival; CSS update; next paint; long tasks; dropped/late frames; React commits; native BrowserView bounds IPC; terminal fits |
| Stream and scroll | snapshot parse; stable-list/grouping work; Markdown/highlighting time; scroll layout reads; rendered item count |
| Startup and navigation | renderer ready; first usable session surface; session switch; active/hidden subscription counts |

Use enough warm-up and repetitions to report distributions, especially p50 and
p95, rather than a single best run. Keep fixture measurements clearly labelled
as synthetic. Run a smaller equivalent path in a real Electron window with an
isolated AO data directory.

The existing chat benchmark is a starting point, not sufficient coverage: it
measures received-to-visible streamed text and CDC scheduling, not submit-to-row
latency or native desktop resize frames.

### 2. Make message submission feel immediate

Add a local, pending representation of the submitted human message at submit
time. It should render in the next available frame while the HTTP request runs.

Reconcile that row using the existing client message ID and accepted turn ID:

1. Submit exactly the current text and staged attachment presentation.
2. Insert one local pending row without changing durable ordering rules.
3. On durable snapshot arrival, replace the local row with the authoritative
   message and turn rather than appending a duplicate.
4. On rejection, timeout, or terminal session change, remove or mark the local
   row according to the error policy and restore a usable composer.
5. Preserve idempotent retries, queued sends, branch operations, and the
   accepted-turn handoff guard.

The acceptance criterion is qualitative and testable: a healthy or slow daemon
cannot delay acknowledgment of the user's own submit. Exact p95 targets will be
set after the baseline, with local echo measured independently from daemon and
provider latency.

### 3. Make rail resizing frame-stable

Profile the existing per-frame path in each inspector mode before deciding which
work to move or throttle. The candidate interventions are:

- prevent direct-drag frames from starting competing Motion/layout animations;
- avoid redundant bounds IPC when the native BrowserView rectangle is unchanged;
- coalesce expensive terminal fitting without leaving the terminal at stale
  dimensions, then perform an exact final fit on pointer release;
- keep the visual rail position synchronized with the pointer even when native
  work is delayed; and
- verify sidebar resizing separately because it affects the shell and can move
  the central session surface.

Do not choose a fixed IPC or terminal-fit interval in advance. Trace data must
show that it is a material source of late frames, and the final interaction must
still keep Browser and terminal content correctly clipped and sized.

### 4. Reduce long-conversation renderer work

Profile before changing behavior. Prioritize work that occurs on every durable
snapshot or stream update:

- full-snapshot parsing and structural identity comparison;
- item filtering, grouping, and memo boundaries;
- Markdown rendering and syntax highlighting;
- activity runs and changed-file summaries; and
- scrollbar/minimap geometry work.

The previous pass already keeps unchanged item identity, bounds streamed text
display lag, caches anchor geometry, and moves very large highlighting work to a
worker. Continue from those constraints. Consider CSS containment or selective
rendering only if profiling proves the remaining history DOM is the bottleneck,
and retain find, text selection, scroll anchoring, and accessible navigation.

### 5. Audit global responsiveness and startup

Review the broader sources of renderer contention:

- CDC/SSE invalidation fan-out, reconnects, polling, and duplicate active
  queries;
- store subscriptions that rerender the persistent shell or hidden panels;
- Electron main/renderer IPC, especially BrowserView lifecycle and bounds work;
- terminal output and fitting under layout changes; and
- startup bundle/module work and background effects that compete with the first
  usable surface.

Rank every candidate by measured user-visible latency and expected blast radius.
Avoid broad architectural rewrites unless evidence shows a local fix cannot meet
the agreed budget.

### 6. Lock in the improvements

Add deterministic tests for instant local echo, durable reconciliation, rejected
send cleanup, and no duplicate rows. Keep timing-sensitive desktop traces as
opt-in trend artifacts unless a metric is stable enough for CI. Require each
performance change to report before/after methodology, environment, p50/p95
values, and known limits.

## Delivery order

1. Baseline and trace the two reported workflows.
2. Implement and verify message local echo/reconciliation.
3. Implement and verify the measured resize bottleneck fix.
4. Address the highest-cost long-conversation or global renderer path found by
   those traces.
5. Add regression coverage and publish the before/after evidence.

Each item should be small enough to review independently. The plan deliberately
does not claim backend, renderer, or native-view blame before the baseline
separates them.

## Evidence and related material

- [Chat responsiveness measurements and methodology](chat-responsiveness/README.md)
- [Conversation send and refresh path](../../frontend/src/renderer/hooks/useConversation.ts)
- [CDC event transport and invalidation batching](../../frontend/src/renderer/lib/event-transport.ts)
- [Shared pointer resize hook](../../frontend/src/renderer/hooks/useResizable.ts)
- [Inspector layout and native BrowserView integration](../../frontend/src/renderer/components/SessionView.tsx)
- [Native BrowserView measurement and bounds updates](../../frontend/src/renderer/hooks/useBrowserView.ts)
