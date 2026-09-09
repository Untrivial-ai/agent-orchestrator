# AO browser profiles

Read this reference when a task depends on authentication, persistent cookies,
profile imports, profile sharing, or switching a worker's browser profile.

## Temporary worker profiles

Without a named profile, AO creates a unique memory-backed Electron partition
for the browser session. Tabs in the worker share it; other workers receive
different temporary partitions. Its cookies and storage are not intended to
survive destruction of the browser session.

## Named persistent profiles

The user can create, select, rename, clear, delete, or import a named browser
profile through AO's Browser toolbar and browser-profile settings. Named
profiles use persistent Electron partitions under AO's data directory.

Workers bound to the same named profile can share its cookies and site storage.
They do not share tab objects, navigation state, active-tab selection, or
`WebContentsView` instances.

Treat the Browser panel's reported profile as authoritative. Do not assume a
worker is temporary simply because the CLI did not select a profile.

## Authentication and imports

Profile selection and import are user-facing operations. A source-browser import
can copy sensitive browsing state, so never initiate one without the user's
explicit request and source-profile choice. Never read credentials or expose
cookies/tokens from either the AO profile or source browser.

At a login wall:

1. Do not type passwords or MFA codes.
2. Ask the user to authenticate in the shared pane.
3. If appropriate, mention that a named/imported profile can preserve login
   state, but leave selection and import consent to the user.
4. Continue only within the authenticated scope of the requested task.

## Switching and clearing

Profile switches rebuild the worker's live tab views because Electron storage
partitions cannot be swapped in place. Wait for agent and browser activity to
finish before switching. During the rebuild, commands may report a profile
active or profile switching error.

Clearing or deleting a profile is destructive. Confirm the exact profile and
user intent immediately before doing so. A live/in-use profile may reject the
operation until its browser work completes.
