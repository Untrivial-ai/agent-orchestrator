---
name: agent-orchestrator-design-system
description: "Use for any renderer UI, visual polish, component, layout, theme, motion, or interaction change in Agent Orchestrator. Read before proposing or implementing visual work."
---

# Design system — Agent Orchestrator

Agent Orchestrator (AO) is serious desktop software for supervising parallel AI coding work. It makes the state of a project legible: what is running, what needs a human, what is safe to ship, and where intervention will have the most leverage.

This is a product UI, not a marketing site. It must feel calm under sustained use, reward scanning, and preserve the terminal as a first-class working surface.

## Reference boundary and migration stance

This guide is deliberately based only on these approved visual references: the app shell; sidebar and its project section; tooltips; iconography; typography; tokens and themes; borders, shadows and outlines; the New Task, Clone Repository, Settings and Add Project flows; dropdowns used in Settings and New Task; and the kanban board with its cards.

Do **not** infer a visual rule from another AO screen merely because it exists today. Much of the application is still being de-slopped. Existing rounded cards, isolated borders, shadows, all-caps metadata, extra helper copy, or one-off button styling outside the approved references are migration debt, not precedent.

The guide is a future-state standard and a review tool. When current code conflicts with it, preserve functional behavior but move the surface toward this standard in the smallest safe change. Do not copy a legacy inconsistency into new work.

### Reference implementation map

Use these files to inspect the approved references. They show current behavior and component boundaries; this guide decides which details are intentional future-state rules.

| Surface | Primary reference files |
| --- | --- |
| App shell, page layout, topbar | [center-panel shell](frontend/src/renderer/components/CenterPanelShell.tsx), [topbar](frontend/src/renderer/components/ShellTopbar.tsx), [shell styles](frontend/src/renderer/styles.css) |
| Sidebar, sidebar buttons, project rows | [Sidebar](frontend/src/renderer/components/Sidebar.tsx), [sidebar primitive](frontend/src/renderer/components/ui/sidebar.tsx), [topbar button](frontend/src/renderer/components/TopbarButton.tsx) |
| Tokens, themes, typography | [tokens](frontend/src/styles/tokens.css), [renderer semantic styles](frontend/src/renderer/styles.css), [site-theme tokens](frontend/src/site-theme/tokens.css) |
| Tooltips, icons, buttons | [Tooltip](frontend/src/renderer/components/ui/tooltip.tsx), [Button](frontend/src/renderer/components/ui/button.tsx), [icon usage](frontend/src/renderer/components/icons.tsx) |
| Shared dialog and field primitives | [Dialog](frontend/src/renderer/components/ui/dialog.tsx), [Input](frontend/src/renderer/components/ui/input.tsx), [Label](frontend/src/renderer/components/ui/label.tsx) |
| New Task and its selectors | [New Task dialog](frontend/src/renderer/components/NewTaskDialog.tsx), [Task composer](frontend/src/renderer/components/TaskComposer.tsx), [settings option menu](frontend/src/renderer/components/settings/SettingsOptionMenu.tsx) |
| Clone Repository and Add Project | [Clone Repository](frontend/src/renderer/components/CloneRepositoryDialog.tsx), [Add Project flow](frontend/src/renderer/components/CreateProjectFlow.tsx), [agent sheet](frontend/src/renderer/components/CreateProjectAgentSheet.tsx) |
| Settings and settings selectors | [Settings dialog](frontend/src/renderer/components/SettingsDialog.tsx), [global settings](frontend/src/renderer/components/GlobalSettingsForm.tsx), [project settings](frontend/src/renderer/components/ProjectSettingsForm.tsx), [Select](frontend/src/renderer/components/ui/select.tsx), [Dropdown menu](frontend/src/renderer/components/ui/dropdown-menu.tsx) |
| Kanban board and cards | [Board route](frontend/src/renderer/components/SessionsBoard.tsx), [renderer adapter](frontend/src/renderer/components/SessionsBoardAdapters.tsx), [shared board/card view](packages/product-ui/src/SessionsBoardView.tsx), [board state presentation](frontend/src/renderer/lib/session-presentation.ts) |

When a referenced file and this guide disagree, follow this guide for new work and log a targeted migration rather than spreading the discrepancy.

## 1. Product and reader

- **Reader:** an engineer coordinating several coding-agent sessions, often while switching among repositories, terminals, pull requests, and reviews.
- **Reader's job:** understand the current state in seconds; open the right session; make a precise intervention; return to the board without losing context.
- **Core object:** a session. Each session carries a task, agent, worktree, conversation or terminal, files, PR, checks, review facts, and derived status.
- **Memorable promise:** “I can run several agents without losing the thread.”
- **Product posture:** operational, technically literate, compact, direct. AO does not pretend that software delivery is effortless; it makes the work visible and tractable.

### Working preferences for design changes

- Prefer direct implementation over a long planning conversation when the intended change is clear. Inspect the existing component and nearby primitives first, then make the smallest coherent change.
- Keep updates and handoffs concise. Explain the user-visible result, meaningful tradeoffs, files or surfaces affected, and verification status; do not narrate routine tool use.
- Preserve unrelated work in a dirty worktree. Do not reset, overwrite, or broaden a change without explicit scope.
- Reuse existing primitives, tokens, and interaction patterns before proposing a new abstraction. If a visual mismatch is local, fix it locally.
- Verify visual changes with the narrowest relevant tests and, when possible, the real renderer surface. Report pre-existing verification blockers separately from regressions introduced by the change.
- Treat repeated feedback as a design rule. When a preference recurs—compact copy, sentence case, alignment, no redundant subtext—encode it here instead of solving it as a one-off.

### Non-negotiable hierarchy

When design requirements compete, preserve them in this order:

1. Truthful operational state and safe actions.
2. The user's active task and their next useful action.
3. Terminal, conversation, file, PR, and review context for the active session.
4. Fast scanning across projects and sessions.
5. A coherent visual system and refined motion.

Never use visual polish to obscure a state transition, destructive action, connection failure, or uncertainty.

## 2. Visual thesis

**Dark-first precision instrument.** AO is a low-glare, near-neutral control surface with calm typography, disciplined color, and dense but breathable information. The visual result should feel more like a thoughtful desktop tool than a generic SaaS dashboard.

- **Decoration:** minimal. Type, alignment, spacing, surface tone, and only necessary one-pixel boundaries establish hierarchy.
- **Material:** layered charcoal surfaces—not pure black—with small tonal shifts reserved for nesting, hover, and elevation.
- **Color:** rare, semantic, and stateful. A colored element must answer “what does this mean?”
- **Shape:** modest radius, not a field of pills. A rounded control should read as a control; a panel should read as one composed container; ordinary metadata should remain text.
- **Density:** compact by default. AO is used all day and needs high information capacity without cramped target sizes.
- **Personality:** quiet competence. No gradients, glass, glow, stock imagery, decorative textures, emoji, or novelty visual language in the working UI.

## 3. App architecture expressed visually

AO has one persistent shell; routes replace the center surface rather than replacing the app's orientation.

### Shell

- **Sidebar:** persistent project navigation and global utility. It answers “where am I?” and “what can I switch to?”
- **Center:** the primary task surface: home, board, session, terminals, settings, or setup.
- **Inspector:** contextual detail and secondary actions. It is collapsible, resizable, and must never make the center task unreadable.
- **Topbar/titlebar:** orientation and high-value actions only. It must not duplicate the entire page's controls.

The layout is desktop-first. On constrained widths, preserve task content first, compact or hide sidebar chrome second, and collapse secondary inspector content before reducing control targets below usable size.

### Route rules

- **Home:** intentionally minimal. It introduces the next meaningful action, not a fake dashboard.
- **Board:** the operational overview. Each lane has a semantic reason to exist and derived status determines placement.
- **Session:** the working room. The conversation or terminal is primary; tabs, files, PRs, and inspector are supporting context.
- **Terminals:** a dedicated surface for standalone shells; do not accidentally route users here from unrelated project-board shortcuts.
- **Settings:** a deliberate configuration surface with grouped sections, explicit save feedback, and no dashboard chrome masquerading as preferences.

## 4. Navigation and project rows

The sidebar is a compact directory, not a second dashboard.

- Use a quiet sentence-case section label only when a section needs disambiguation. Prefer no label to ornamental chrome. Never use ALL CAPS labels.
- Projects form one shared list container: no card gap and no individual row border. Use a subtle divider only between sibling rows.
- Project rows are compact: 28–32px visual height in expanded rail, 12–13px label text, and 14px folder/provider icon. Keep the hit target at least 32px high.
- Project icon/avatar and label are the primary scan anchors. Do not place status pills, branches, diffs, or PR counts in the row by default.
- Expanded sessions are visibly children through indentation and reduced contrast, never through heavy nesting or permanent decorative rails.
- The active project/session uses restrained surface contrast plus a clear selection cue; blue is acceptable for focus/active edge, not as a broad row fill.
- Row actions appear on hover or focus without moving the label. Actions must not change the row's measured width or trigger navigation.
- The collapsed rail is icon-first and keeps tooltips, keyboard focus, and fixed titlebar controls usable.

## 5. Operational state and color

Status is derived from durable runtime, PR, CI, and review facts. The renderer communicates it; it does not invent or store a separate display truth.

### Semantic mapping

| Meaning | Token / visual treatment | Use |
| --- | --- | --- |
| Working | `--color-status-working` / #60a5fa | active spinner, live activity, progress that is currently observed |
| Needs human input | `--color-status-needs-you` / #f06445 | blocked, question, actionable interruption |
| Validating | `--color-status-validating` / #facc15 | checks or verification in progress |
| Review / caution | `--color-status-in-review` / #f59e0b | review pending, changes requested |
| Ready / success | `--color-status-ready` / #4ade80 | mergeable, approved, completed actionable success |
| Merged | `--color-status-merged` / #c084fc | historical terminal completion state |
| Failure | `--color-status-exited` / `--destructive` | failed CI, destructive or failed operation |
| Idle / unknown | muted or passive tokens | neutral, not a hidden failure |

### Status glyph rules

Use one fixed status slot where status belongs. Prefer an icon/glyph over a prose badge.

1. If the session is actively working, show the working spinner.
2. Otherwise, if a PR is relevant, show the PR glyph tinted by its actionable state.
3. Otherwise, show a dot: amber/red for attention, muted for idle/complete.

Do not show multiple competing status chips on a session card or sidebar row. Text labels are allowed when needed for accessibility, an empty state, an inspector summary, or a decision the user must make.

## 6. Tokens: source of truth

Do not introduce one-off hex values, arbitrary radius, or unreviewed spacing. Use `frontend/src/styles/tokens.css` and the semantic aliases in `frontend/src/renderer/styles.css`.

### Surface stack

- Canvas: `--color-bg-primary`
- Raised card / grouped content: `--color-bg-secondary`
- Hover or selected sub-surface: `--color-bg-tertiary` or `--color-interactive-active`
- Popover / modal elevation: `--color-bg-elevated`
- Sidebar: `--color-bg-sidebar`
- Terminal: `--color-bg-terminal-opaque` as a painted surface; preserve terminal-specific text colors
- Boundaries: `--color-border` for ordinary hairlines; `--color-border-strong` only when separation must survive dense content

### Text stack

- Primary content: `--color-text-primary`
- Secondary explanation: `--color-text-muted`
- Passive metadata / de-emphasized chrome: `--color-text-passive`
- Code and links: `--color-text-markdown-code` and `--color-text-markdown-link`
- Never reduce essential task information to passive contrast.

### Accent rules

- `--color-accent` is for the live edge: primary action, focus ring, meaningful selected state, or progress.
- Success, warning, and destructive colors never decorate neutral UI.
- Do not use color as the only state signal. Pair it with a distinct icon, label, position, or action affordance.

## 7. Typography

Use the loaded Geist families. Do not add a new web font or substitute a generic font for visual novelty.

- **UI and body:** `--font-family-base` (`Geist Variable` first).
- **Code, terminal, compact labels:** `--font-family-mono` (`Geist Mono Variable` first).
- **Weights:** 500 medium for controls and small hierarchy; 600 only for headings, selected values, and genuinely primary emphasis.
- **Numbers:** use tabular figures for metrics, timestamps, diffs, and columns where alignment matters.

### Type scale

| Role | Token | Intended use |
| --- | --- | --- |
| Nano | 8px | exceptional compact rail chrome only |
| Micro | 10px / `--font-size-xs` | compact metadata and tight pills |
| Caption | 11px / `--font-size-caption` | lane labels, secondary labels, compact metadata |
| Secondary | 12px / `--font-size-sm` | helper copy and compact list detail |
| Dense control | 13px / `--font-size-base` | buttons, board controls, project rows |
| Body | 14px / `--font-size-md` | readable prose and normal UI content |
| Dialog title | 15px / `--font-size-subtitle` | modal and section titles |
| Empty state | 17px / `--font-size-heading-sm` | short guidance headings |
| Page heading | 21–22px | route-level title only |

Use sentence case everywhere a user reads or acts: page titles, buttons, menu items, field labels, section labels, statuses, and compact metadata. Mono is for code-like values (branches, paths, repository URLs, commands, aligned numbers), not for making ordinary UI feel technical. Never use ALL CAPS labels.

## 8. Spacing, dimensions, and shape

### Spacing

The base unit is 4px. Use the token scale, with 4/6/8/10/12/14/16/18px as the normal dense rhythm. Use 24/32/40px for meaningful section separation, not for routine card padding.

- In a component, keep label → value → helper text close.
- Between peer controls, use 4–8px.
- Between related groups, use 12–16px.
- Between distinct regions, use 20–32px.
- Avoid gaps that only exist to make cards feel separate; let containment and dividers do the work.

### Fixed dimensions

- Compact icon control: 20–24px.
- Standard compact control: 28–32px.
- Board action: 30–36px.
- Minimum pointer target for an ordinary row: 32px; use 36px when the interaction is high-frequency or imprecise.
- Default sidebar: 240px; icon rail: 48px; mobile overlay: 288px.
- Standard topbar row: 36px; Windows shell topbar: 56px.

### Radius and borders

- Borders are exceptional structure, never default decoration. Start with spacing, alignment, type, and a restrained surface shift.
- A hairline is 1px and may separate durable regions: shell panes, a modal from its backdrop, a shared list's adjacent rows, a board lane boundary, or an input's hit area when contrast needs it.
- Do not give every card, row, input group, and nested region its own outline. Never add a border simply because a component is rectangular.
- `--radius-xs` and `--radius-sm` suit tiny controls; `--radius-md` suits buttons and inputs; `--radius-lg` is the maximum routine component radius.
- `--radius-panel` is reserved for route-level framed surfaces and larger dialogs.
- `--radius-full` is only for avatars, dots, and intentional pills.
- Shadows are elevation cues for transient layers only: dialogs, menus, and tooltips. They are subtle, short-range, and neutral. No permanent card shadows, glow, or decorative drop shadow.
- Focus is not decoration: use the semantic focus ring/outline with sufficient contrast. Never remove it or replace it with a barely visible background shift.
- Avoid cards inside cards. If hierarchy is weak, fix spacing, typography, or surface contrast before adding another border.

## 9. Components and interaction contracts

### Buttons

- One clear primary action per focused region. It uses accent fill or strong foreground contrast.
- Secondary actions use a quiet surface; tertiary actions are text/icon controls. An outline is a contrast fallback, not the default secondary-button style.
- Icon-only buttons require an accessible label and tooltip when their meaning is not universally obvious.
- Destructive actions require destructive color and confirmation when the operation is difficult to reverse.
- Disabled states explain why when the missing prerequisite is not obvious.

### Tooltips and iconography

- Use Lucide icons through the existing shared icon/button primitives. Icons are functional marks, never decoration or a substitute for a missing information hierarchy.
- Default UI icons are 14–16px; use 12px only for dense metadata and 18–20px only for a high-level source choice or empty-state action. Do not enlarge an icon merely to fill empty space.
- Keep stroke weight, optical size, and alignment consistent with nearby controls. Do not mix filled, multicolor, emoji, or custom illustration styles into the working UI.
- A tooltip names an icon-only action, disambiguates a truncated value, or reveals a compact metric. It must be a short phrase, not a paragraph, help article, or duplicate of visible text.
- Tooltips appear after the existing deliberate hover delay, never block pointer interaction, and remain available to keyboard focus. They should use elevated surface contrast and at most a subtle transient elevation—no dramatic shadow, animation, or callout treatment.

### Inputs, dropdowns, menus, and dialogs

- Labels precede inputs. Placeholder text is an example, never the sole label.
- Validation appears close to the field and uses both wording and semantic color.
- A select/dropdown trigger is one compact control: selected value first, chevron second; it should not add a second visible label when the surrounding field label already establishes meaning.
- Dropdown content is a single elevated surface with a quiet edge, consistent item height, clear selected/focus state, and dividers only between meaningful groups. Never make each option a separately bordered mini-card.
- Settings and New Task selectors use the same shared menu/select primitive and option rhythm. Model, agent, mode, and settings choices must not invent bespoke pills or popovers.
- Menus group related actions, separate destructive actions once, preserve focus order, and close after a completed single-choice action. Do not use a tooltip to explain a menu item that can be named clearly.
- Dialogs interrupt for a real decision; do not use them for ordinary navigation or success confirmation.
- Dialogs retain the route context beneath them and restore focus to the trigger on close.

### Modal composition

Every modal is one composed, elevated surface: a restrained overlay, one clear title, a close control, the decision/form body, and a concise action area when needed. Modal content has no ornamental header band, no nested card stack, no gradient, and no explanatory prose unless it changes the user's decision.

- **New Task:** a single title line and the composer. The task input is the visual center; agent/model/mode controls are compact supporting choices. Do not repeat instructions the composer itself makes obvious. Submission feedback must stay in place and not rearrange the composer.
- **Clone Repository:** compact back, title, and close controls establish its place in the Add Project sequence. Repository URL is code-like and uses mono; owner/provider identity may be an unobtrusive visual cue. Destination is one full-width chooser row with its action embedded at the trailing edge. Validate after intent to continue and place field errors directly below the field; do not turn errors into a large alarm card.
- **Add Project:** present one decision at a time. The source picker is a shared container of adjacent choices with dividers—not three floating cards. Each row has a modest icon, a concise source name, and only the one line of detail needed to distinguish Clone, Local, and Workspace. Local/Cloud is a compact segmented choice; show the one decision-relevant hint below it only while the choice is ambiguous. Cloud sign-in and project creation remain part of this same flow rather than a competing dashboard.
- **Settings:** use one modal split into a quiet navigation rail and a content pane. The rail is a list of compact icon-and-text rows with a restrained selected surface; it is not a collection of pills. The content pane gives the active section one sentence-case title, fields in logical groups, and no redundant introduction. For project settings, keep the persistent save action anchored in the rail/footer position so it is predictable and does not float beside every field.
- **Errors and status:** show error text close to the failed action or field. Reserve a full-width alert region for a blocking, form-level failure; it is a state treatment, not a permanently outlined component.

#### Settings language and agent-management rules

- Settings names describe the user's job, not internal vocabulary. Use **Agents** for installing and configuring agent tools, and **Accounts** for signing in and managing Codex accounts. Do not use “Harness” or “Subscriptions” as navigation labels for these surfaces.
- Remove introductory copy when the title, control, or nearby status already explains the task. Keep only text that changes a decision, explains a failure, or provides a required next step.
- Use concise, sentence-case labels and ordinary search placeholders such as “Search agents”. Avoid trailing ellipses in simple search fields and avoid repeating the same context in a label, placeholder, and subtext.
- Agent installation rows use one shared install control. The installed state is terminal: show a disabled **Installed** button, hide installation details, and do not show a separate check icon or retry/details affordance.
- When a completed install method is trustworthy, the secondary line may say “Installed via Homebrew”, “Installed via npm”, or “Installed via Official”. Never display internal cache fingerprints, hashes, or other implementation identifiers as user-facing versions.
- Installation details are an expandable disclosure below the row's primary controls. Animate the details region between collapsed and expanded states, keep the disclosure control below the details, and align the content with the agent icon/list edge.
- Icon-only actions must have an accessible name and a tooltip when their meaning is not obvious. If a text action already fits naturally, prefer the text action over an icon-only control.

### Kanban board and cards

- The board is the project’s operational overview, not a KPI dashboard. The canonical delivery lanes are Working, Needs you, In review, and Ready to merge; archive is a separate history state.
- Lanes are one continuous grid separated by shared vertical dividers, with a compact header: semantic dot, sentence-case name, and count. Do not turn every lane into a rounded panel.
- A board card represents one session—the legitimate exception to “no individual borders.” It is an actionable object with a bounded, draggable/clickable area. Keep its edge quiet; attention is expressed by the single semantic status treatment, not extra colored strips, badges, or shadows.
- Card information order: agent avatar + task title; branch only when it adds identity; PR/review evidence only when present; one derived status line; then compact time/usage metadata. Avoid duplicate status words, repeated provider names, and metric clutter.
- Title gets the visual weight. Avatar is small and recognisable, branch is mono and muted, PRs are compact evidence links, and timestamps/numbers use tabular figures where they scan in columns.
- One fixed action slot may appear on hover/focus without shifting the title. Destructive action remains visually quiet until intentional hover/focus but always keyboard reachable.
- Needs-human attention may use the dedicated semantic color and restrained motion. Do not pulse multiple cards, animate lane counts, or make success/caution states compete for attention.

### Shared-list rule

When several peer objects are primarily chosen or scanned—projects, settings sections, source choices, dropdown options—use a shared surface with adjacent rows and subtle dividers where necessary. Use individual cards only when each object carries a distinct, actionable operational payload, as board sessions do.

### Terminal and conversation

- The terminal is a working surface, not a decorative dark panel. Preserve its own palette, contrast, scroll behavior, and selection semantics.
- Never layer translucent UI or decorative gradients over live terminal content.
- Chat and TUI are alternate interfaces for the same session. Their surrounding orientation and actions must remain coherent across mode changes.

## 10. Motion, transitions, and feedback

Motion is functional and modest.

- Fast feedback: `--duration-fast` (120ms) for hover, color, icon, and small opacity changes.
- Normal movement: `--duration-normal` (150ms) for small layout and panel state changes.
- Sidebar and route shell may use the established ~280ms settle transition where layout must remain spatially legible.
- Use ease-out for entry, ease-in for exit, and ease-in-out for positional movement.
- The working spinner/pulse may animate to show liveness. Do not animate text, metrics, or layout merely to make the interface feel busy.
- Respect reduced-motion preferences: remove nonessential transforms and reduce transitions to immediate state changes.

## 11. Platform and responsive behavior

- **macOS:** maintain traffic-light clearance and the fixed titlebar navigation cluster. The shell topbar may be hidden when session-local chrome owns orientation.
- **Windows/Linux:** shell topbar spans above the sidebar; preserve native/window-titlebar accommodations.
- **Narrow center panels:** compact secondary actions, then inspector content, before hiding the task itself.
- **Sidebar:** may collapse to its icon rail or an overlay; every icon must remain labeled through tooltip and accessible name.
- **Inspector:** collapses to zero without reflowing the active terminal/chat into an unusable transient width.
- **Keyboard:** every action that is visible on hover must be reachable by focus; shortcuts must respect the current route and never trigger a surprising cross-surface navigation.

## 12. Accessibility and resilience

- Meet contrast requirements in both dark and light themes; test real token pairs rather than assumed hex values.
- Visible focus rings use the semantic focus/ring token and are never removed for mouse-centric aesthetics.
- Communicate loading, error, offline, empty, and permission states explicitly.
- Preserve text alternatives for icons, provider avatars, status glyphs, and action-only buttons.
- Do not infer that a session is dead from an absent or failed runtime probe; the UI must communicate uncertainty accurately.
- Preserve spatial continuity while data streams: avoid flicker, sudden reorder, and broad skeleton replacement for a small local update.

### Interaction lifecycle to design and test

For every approved surface, design the full lifecycle rather than only the happy-path screenshot:

| Moment | Required question |
| --- | --- |
| Arrive | Is the purpose and next action clear without tutorial copy? |
| Leave untouched | Is there a calm, complete default state with no fake urgency? |
| Begin | Does focus move predictably and does the control state make the pending decision obvious? |
| Active / submitting | Is progress local, truthful, and free of layout shift or duplicate submit affordances? |
| Finish | Does success move the user forward naturally; does failure explain the actionable next step in place? |
| Interrupt | Do Escape, close, navigation, lost focus, daemon/network failure, disabled actions, and changed project/session state preserve user work and restore a predictable focus target? |

Test meaningful variants: dark/light theme, expanded/collapsed sidebar, long project or task text, keyboard-only use, narrow central pane, loading/error/empty state, and a state change arriving while the surface is open.

## 13. Explicit anti-patterns

Reject these patterns in renderer work unless a specific exception is approved:

- Generic SaaS hero/feature-grid language inside the application shell.
- Decorative gradients, glows, glass, blobs, textures, or ornamental shadows.
- ALL CAPS labels, uppercase tracking used as decoration, or mono used to make ordinary prose look technical.
- A rainbow of status colors, decorative accent borders, or color-only meaning.
- Oversized avatars or icons that compete with task names and state.
- Large empty padding that lowers operational density without aiding comprehension.
- Uniform rounded cards separated by gaps when a shared container with dividers better expresses a list.
- A border around every row, field, option, card, and nested panel; stacks of outlined containers; or a drop shadow on permanent content.
- Repeated labels, helper paragraphs, captions, or subtext that merely restate an obvious control or choice.
- A new button, select, tooltip, menu, dialog, or icon treatment when the matching shared primitive already exists.
- Repeated metric tiles when one table, timeline, or state glyph is clearer.
- Tiny low-contrast explanatory copy that conceals an important system state.
- Fake terminal screenshots, fake progress, or generic “AI” illustration.
- Moving controls on hover, layout shifts on data refresh, or shortcut behavior that changes routes unexpectedly.

## 14. Implementation rules

- Start with existing renderer components, shadcn primitives, semantic tokens, and nearby patterns.
- Do not create a parallel token system, raw inline color palette, or one-off global style for a single screen.
- Keep visual behavior in the renderer; daemon facts and lifecycle logic remain in the backend.
- For a new named token, prove that existing semantic roles cannot express the need. Add it to `frontend/src/styles/tokens.css`, then expose it through `styles.css` only when Tailwind/component usage requires it.
- For visual changes, test dark and light themes, compact sidebar, narrow center panel, keyboard focus, disabled/loading/error states, and at least one screen with real long project/session names.

## 15. Design QA checklist

Before calling a visual change done, verify:

1. Does the reader see the active task and next useful action before secondary metadata?
2. Is every color semantic and every status truthful?
3. Did we reuse existing tokens and primitives?
4. Is every word sentence case, necessary, and at the right point in the decision—not a label or subtext duplicate?
5. Did we remove default borders/shadows and then add back only the structural ones this surface genuinely needs?
6. Are peer choices a shared list with dividers rather than spaced, individually bordered cards?
7. Are dense lists grouped and scan-friendly without becoming cramped? Are project rows compact enough for an all-day sidebar?
8. Does focus work without hover? Does reduced motion remain coherent? Are icon-only actions named by accessible label and, where needed, tooltip?
9. Does the change hold on dark/light themes, all desktop platforms, compact sidebar, narrow center width, long text, and loading/error/empty states?
10. Did we avoid introducing a generic dashboard/card-grid pattern where AO already has a stronger operational model?

### Review outcome

Approve only when the answer to every applicable check is yes. Otherwise classify the finding so the next step is concrete:

- **Blocker:** creates misleading operational state, inaccessible action/focus, unsafe destructive affordance, or a new one-off primitive.
- **Must fix:** violates an explicit visual rule: heavy/individual borders, gradient, permanent decorative shadow, ALL CAPS label, noisy helper copy, or duplicated component treatment.
- **Follow-up migration:** a legacy surface outside this reference boundary that should be aligned later; record it without using it to lower the standard for this change.

## Decisions log

| Date | Decision | Rationale |
| --- | --- | --- |
| 2026-09-03 | Dark-first, token-led, compact operational UI | Matches AO's long-running desktop workflow and renderer source of truth. |
| 2026-09-03 | Geist/Geist Mono remain the typographic system | They are bundled, legible at dense sizes, and already define renderer hierarchy. |
| 2026-09-03 | Color is reserved for semantics and active focus | Parallel-agent supervision depends on fast, trustworthy scanning. |
| 2026-09-03 | Shared list containers with dividers are preferred over spaced sibling cards | Better density and clearer grouping for projects and sessions. |
| 2026-09-03 | Existing platform shell behavior is part of the design system | macOS traffic lights, Windows titlebar, and collapsible inspector are product behavior, not incidental CSS. |
| 2026-09-03 | The approved reference set is intentionally narrow | AO is mid-migration; unfinished screens must not become accidental design authority. |
| 2026-09-03 | Sentence case and concise decision-relevant copy are mandatory | The interface should carry operational clarity through hierarchy and state, not verbose explanation or technical-looking labels. |
| 2026-09-03 | Borders and shadows are structural/elevational exceptions | The future UI favors one composed surface, shared lists, quiet dividers, and stable hierarchy over a field of outlined cards. |
| 2026-09-04 | Settings language follows the user's task, not internal architecture | “Agents” describes installation/setup and “Accounts” describes sign-in/account management more clearly than “Harness” or “Subscriptions”. |
| 2026-09-04 | Installed agent rows use a terminal, compact state | A disabled Installed button is clearer than a detached check icon; stale details must not make a completed installation look actionable. |
| 2026-09-04 | Only trustworthy user-facing data belongs below an agent name | The model catalog's `binaryVersion` is an internal cache fingerprint, so it must not be presented as a CLI version. |
