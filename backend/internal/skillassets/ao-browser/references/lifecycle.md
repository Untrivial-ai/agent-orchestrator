# AO browser lifecycle

Read this reference when browser behavior depends on when native views are
created, retained, rebuilt, or destroyed.

## Ownership and registries

Electron's main-process `BrowserViewHost` is authoritative for the human-facing
browser. It centrally tracks:

```text
session ID -> browser session -> tab ID -> WebContentsView
```

It also keeps reverse mappings from session IDs and Electron webContents IDs so
renderer events, browser signals, and agent commands reach the correct worker.

The named-profile store is a separate durable registry. It contains profile
metadata and worker-to-profile bindings, not live tabs or `WebContentsView`
objects.

The `agent-browser` automation runtime keeps its own target state. AO selects
and synchronizes that runtime target against the BrowserViewHost's active tab
before commands. The two registries are intentionally separate and can drift;
known selection and close paths resynchronize or fall back safely.

## Creation

A session browser is created lazily when either:

- the renderer's browser hook ensures the worker view, or
- an agent executes the worker's first browser command.

If no live entry exists, AO resolves the worker's profile binding, creates a
browser-session entry, constructs its first `WebContentsView`, attaches the view
to the main window off-screen, and loads `about:blank`. Later ensures reuse it.

Each additional AO tab creates another `WebContentsView` in that browser
session. Tabs share the session's current Electron storage partition.

## Visibility and worker switching

The Browser panel's controls live in React, while the page itself is a native
Electron child surface. The renderer measures the DOM placeholder and sends its
window-relative bounds to the main process.

When the panel is hidden or the user switches workers, AO normally moves the
native view off-screen and marks it invisible without destroying it. Switching
back reuses the registered session, preserving its live tabs and navigation.
Agent commands can continue using the session while the panel is hidden.

AO does not currently enforce a hidden-browser LRU cap. Avoid unnecessary tabs
and sessions during automation.

## Profile switching

A `WebContentsView` cannot change its Electron partition in place. When the user
switches profiles, AO waits for active browser work, remembers allowed tab URLs
and selection, closes the session's automation runtime, destroys the old views,
creates replacement views using the new partition, reloads remembered URLs,
and restores selection.

Commands are rejected while that transition is active. Wait for it to finish;
do not open an unrelated browser to bypass it.

## Destruction

AO destroys the complete session-owned browser when the worker terminates, the
session receives an explicit internal destroy operation, the browser host is
disposed, or the desktop app closes. Destruction closes the automation runtime,
removes registry mappings and native child views, and closes tab webContents.

Named profile storage can survive, but live tabs and views are in-memory and are
not reconstructed from the profile registry after an application restart.
