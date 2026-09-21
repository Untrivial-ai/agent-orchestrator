# Session Rename Surfaces

## Goal

Make session renaming easy to discover and consistent in the two places where a session name is used for navigation: the project sidebar and the owning session tab above the terminal.

## Interaction

- Double-clicking the session name in either surface starts inline editing.
- Right-clicking either surface opens its existing context menu with a **Rename** action that starts the same inline editor.
- Rename mode preserves the dimensions and visual treatment of the surrounding row or tab. The focused text input uses the existing compact input, focus, color, and radius tokens.
- Enter or blur saves a trimmed non-empty name through the existing session rename API.
- Escape cancels without saving.
- Empty or unchanged values leave the existing name in place.
- While editing, navigation, dragging, and other pointer actions from the containing row or tab must not fire.

## Architecture

The daemon endpoint and typed `renameSession` client already exist, so this is a renderer-only change. The sidebar keeps its current inline rename state and persistence flow, but expands the double-click target and exposes Rename in its context menu. The session terminal tab receives the equivalent edit state and uses the same API/query invalidation boundary so both surfaces refresh from the canonical session read model.

Shared behavior should be extracted only if the two real call sites otherwise duplicate meaningful state or save/cancel logic. Styling remains local to each surface because a sidebar row and terminal tab have different layout constraints.

## Errors and synchronization

Failed requests leave the prior server-provided title visible and log through the frontend's existing error path. A successful rename invalidates the workspace query; the daemon's existing change stream continues to synchronize other clients. No optimistic durable state or backend/API changes are needed.

## Accessibility

- The input has a session-specific accessible label.
- The context-menu item is keyboard reachable.
- F2 continues to start sidebar rename and should start rename for the focused owning terminal tab where the tab keyboard model permits it.
- Focus moves into the editor on entry and returns naturally to the renamed navigation surface after save or cancel.

## Tests

Component tests cover both surfaces and both entry points:

- double-click enters rename mode;
- context-menu Rename enters the same mode;
- Enter saves the trimmed name through the daemon client and refreshes workspace data;
- Escape cancels;
- editing does not navigate or trigger drag behavior;
- empty and unchanged names do not call the daemon.

Frontend typecheck and build remain the broader verification gates. The real Electron development app is launched against the local AO data only after automated checks pass so the interaction can be tried manually.

## Out of scope

- Renaming standalone shell terminals, reviewer terminals, projects, or agent runtime identifiers.
- Changing the daemon rename endpoint, storage schema, session IDs, or title fallback rules.
- Introducing a modal rename dialog or new visual language.
