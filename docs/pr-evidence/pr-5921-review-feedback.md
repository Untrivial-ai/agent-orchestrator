# PR #5921 frontend review feedback

This records the original file-by-file findings and their cleanup resolution. The “Current behavior” bullets describe the PR before cleanup; the status on each item is authoritative. Preserve the existing local UI; host settings and identity badges are the intended visual additions.

## Follow-up requirement: clean host setup and day-two commands

- A person with a fresh supported remote machine should run one copy-paste setup command (two only if an unavoidable interactive authorization step remains), without manually cloning AO, running `npm ci`/multiple `build:*` commands, installing basic prerequisites one by one, or locating a long path to the daemon binary. The command must install the complete headless AO runtime, configure an always-on service and the selected private/tunnel connection, and print the exact pairing details for the desktop UI. Re-running it should safely update an existing installation. For a released build, prefer a verified prebuilt host artifact over compiling on a 4 GB VM; a branch/pre-release path may build from source behind the same simple entry point.
- Make the installed `ao` CLI easy to invoke and document the already-present `ao status`, `ao remote-host status`, and `ao remote-host enable/disable` commands. The user must be able to inspect daemon/service and tunnel health, the current address, and the connection password on the VM. Add a small diagnostic/log command only where the existing output is insufficient; do not duplicate the same state in a second system.
- Preserve explicit user authentication for the chosen agent provider and GitHub; a setup command must not copy laptop credentials or imply it can create them. Remote harness installation from the desktop UI remains part of the first-day journey.
- Validate this on a clean Ubuntu host, including service persistence after SSH logout/reboot, tunnel address changes, reconnect, resource headroom on the 2-CPU/4-GB VM, host-side harness install/login, project/task, and Git push/PR. The prior manual detours (source build/bundle, cloudflared, lingering, harness/provider setup, GitHub auth, OpenCode database recovery, and VM memory pressure) are the checklist to collapse or explain honestly.
- **Status:** One-command Ubuntu bootstrap and easy `~/.local/bin/ao` link implemented; shell syntax and installer smoke checks pass. Clean-host acceptance is deferred to the user's next VM test; no claim of reboot/resource verification.

## Discussed directions

### 1. Browser annotations must not mutate preview routing

- **Files:** `frontend/src/renderer/components/BrowserPanel.tsx`, `frontend/src/main/remote-proxy.ts`.
- **Current behavior:** The annotation sender recognizes `ao-preview-<hex>.localhost` with a renderer regex and calls `previewUrl` again to check the origin. That call can replace the active preview mapping when its source differs. The renderer also reparses a source URL that the proxy may have accepted in shorthand form.
- **Direction discussed:** Keep viewer URL recognition and canonical source mapping in the proxy. Add a read-only, session-scoped resolution operation that verifies the active viewer origin and returns the source URL with the viewed path, query, and fragment. Annotation submission should not create, revoke, or rotate previews. Preserve the current fail-closed behavior for stale/foreign preview URLs and leave external URLs alone.
- **Status:** Resolved: the proxy exposes a read-only session-scoped URL resolver; annotation submission no longer calls `previewUrl` or parses proxy hostname patterns.

### 2. One terminal UI for local and self-hosted sessions

- **Files:** `frontend/src/renderer/components/CenterPane.tsx`, `TerminalPane.tsx`, `RemoteTerminalView.tsx`, and their data/transport hooks.
- **Current behavior:** `CenterPane` branches on `hostId` to render a separate remote terminal component. It also selects query invalidation and agent-switch data by host. `TerminalPane` already accepts a `createMux` transport, but its provider, retained cache, restore action, links, and workspace queries still assume the local daemon in places.
- **Specific gaps confirmed in `RemoteTerminalView.tsx`:** It attaches every remote handle as `shellTerminalHandleId` with no session, so the shared hook suppresses agent-session workspace invalidation on terminal exit. It omits `onLinkOpen`, leaving ordinary web-link clicks with no AO Browser action, and omits the local terminal's focus request. These are consequences of the duplicate view, not reasons to add another set of remote-only controls.
- **Agreed principle:** `CenterPane` should not decide which visual implementation to render. Select a backend/PTY transport per session in the data layer and reuse the existing terminal presentation. A single global backend URL is insufficient because local, Host A, and Host B can be open simultaneously; cache keys and writes must remain host-scoped.
- **Status:** Resolved: `RemoteTerminalView` and its duplicate test were removed; the shared `TerminalPane` receives a host-selected mux and host-qualified session data.

### 3. Remote clone validation should be honest and host-owned

- **Files:** `frontend/src/renderer/components/CloneRepositoryDialog.tsx`, `CreateProjectFlow.tsx`, `frontend/src/renderer/hooks/usePreparedClone.ts`, `backend/internal/service/project/clone.go`.
- **Current behavior:** A syntactically plausible remote URL is immediately marked `repositoryCheck = "valid"`, enabling Continue without testing access. The remote host's later `git clone` is authoritative and reports `GIT_CLONE_FAILED`, so this is an early-feedback/parity issue, not a bad-project or data-integrity issue. The laptop's `git ls-remote` is not authoritative for host credentials/network.
- **Direction discussed:** Do not represent untested remote access as validated. For local-like early feedback, run the check on the selected host; keep the actual clone as final authority and preserve its error handling.
- **Status:** Addressed without another preflight: remote access is labeled unchecked, never valid; Continue runs host-side clone preparation and reports its authoritative error in this flow. A separate `ls-remote` API would duplicate the network operation and still not guarantee the later clone succeeds.

### 4. CreateProjectAgentSheet.tsx

- **Current PR change:** Fetches agent readiness from the selected host, excludes definite non-launchable agents remotely, avoids using laptop session history to infer remote defaults, blocks creation while disconnected, and routes the Manage harness action to the selected host. It changes no layout.
- **Review finding:** `useSettings()` still reads laptop daemon settings, although `trackerIntakeEnabled` is a daemon-side gate. A remote project's intake control can therefore reflect the laptop rather than the selected host; likely use the existing `useSettings(hostId)` path.
- **Review finding:** The new `"No available agents on this host."` string is hard-coded instead of translated like adjacent errors.
- **Review question:** Opening a remote sheet calls `ensure` with no agent IDs and `purpose: "launch"`. The daemon interprets empty IDs as *all supported harnesses*, so confirm that full launch-grade probing is necessary before selecting one; avoid attributing the VM's earlier load spike to this without measurement.
- **Status:** Resolved: settings are host-aware, the error is translated, and only selected agents receive launch-grade readiness probing.

### 5. CreateProjectFlow.tsx

- **Current PR change:** Reuses the project-creation flow for a selected host. Host-side API calls validate folders, prepare Git, and prepare clones; a remote directory picker replaces the laptop's native folder picker. Offline hosts cannot start the flow, and the remote source dialog names the selected host.
- **Agreed fix:** Workspace import skips the laptop-only repository scan but synthesizes an empty scan. `mergeWorkspaceImportRepos` then displays committed remote child repositories with branch `HEAD` and no origin URL, even when the actual branch and origin differ. Restore accurate host-sourced metadata or omit the inaccurate branch display.
- **Agreed fix:** Remote GitHub repository creation skips the laptop's GitHub owner and availability checks. It requires the user to type an owner and allows Continue without checking whether the repository name is available. Add equivalent host-side early feedback while keeping host-side creation authoritative.
- **Agreed UI decision:** Remove the per-host cards added to the generic New Project source picker. Keep the established sidebar entry, “Add project on <host>,” as the remote-project entry point; do not redesign the local dialog.
- **Status:** Addressed with the existing host operation: misleading synthetic `HEAD` metadata and the generic host cards are removed. Remote GitHub owner/name are required; Continue invokes host-side repository preparation and reports its error. A separate owner/availability API was omitted because it would duplicate the host call and remain subject to a race before creation.

### 6. NotificationCenter.tsx

- **Current PR change:** Combines local, connected-host, and Cloud notifications in the existing bell; host-qualifies notification/session identities, shows host labels, and routes read, clear, restore, OS-alert clicks, and pagination to the owner host.
- **Review finding:** `seenRemoteIds` is replaced with only the latest first page of unread IDs. When a new item pushes an old item off that page and the new item is later marked read, the old item re-enters the page and is treated as a new OS alert. Retain previously seen IDs across page shifts, with a bounded policy if needed.
- **Review finding:** If one remote notification query fails while other rows exist, `remoteLoadFailed` is true but no inbox error is shown. The UI silently omits that host's notifications; show a partial-load warning and retry without hiding healthy hosts' rows.
- **Architecture concern:** Local read/clear operations use notification hooks, but this component directly implements remote read/clear API calls and page state. This diverges from the agreed dumb-UI/data-source approach. Consider making the existing notification operations host-aware in the data layer, while keeping the current bell presentation and per-host labels.
- **Agreed transport direction:** Local notifications use `/api/v1/notifications/stream` over SSE; `useRemoteNotifications.ts` currently polls every remote host's REST endpoint every two seconds. For each remote host connection, attempt the same host-scoped SSE stream once while keeping the current two-second poll as coverage during the probe. Do not infer health from `EventSource.onopen`: the notification stream currently sends no heartbeat, so an intermediary can buffer event frames while appearing connected. Add a small observable ready/heartbeat frame and a bounded timeout. Once delivery is verified, stop polling; if verification fails or a healthy stream later fails, use two-second polling for the rest of that connection lifecycle, without repeated SSE retries. Probe again only after app startup or a real host reconnection/address change. Keep host-qualified cache/identities and the existing bell UI.
- **Status:** Resolved: bounded seen-ID history, partial-load warning, remote read/clear data helpers, and SSE-first with observed-frame fallback are implemented.

### 7. ProjectSettingsForm.tsx

- **Current PR change:** Uses the selected host for project settings, model/readiness queries, save, workspace refresh, and orchestrator replacement. It retains the existing settings form and removes only the invalid local `file://` link for a remote project path.
- **Review finding:** When a remote host disconnects, `hostConnected` becomes false and `SettingsBody` is unmounted in favor of the offline message. `SettingsBody` owns the unsaved form state, so an in-progress edit is lost even if the host reconnects. Preserve the draft while offline and prevent writes until connected, or place the draft above the connectivity-gated rendering.
- **Related review question:** This form also runs launch-grade readiness for every supported agent on opening; review together with the same call in `CreateProjectAgentSheet.tsx`, without assuming it caused the earlier VM load spike.
- **Status:** Resolved: the form and unsaved draft remain mounted through a disconnect; writes wait for reconnection. Only selected agents receive launch-grade probes.

### 8. RemoteAddProjectDialog.tsx

- **Current PR change:** Wraps the existing `CreateProjectFlow` for a selected host, registers the project through that host's API, and starts its orchestrator in the background. There is no new project-creation visual design.
- **Review finding:** Project and repository-initialization API errors are converted to plain `Error`, dropping `apiErrorCode`. `CreateProjectFlow` uses `NOT_A_GIT_REPO` and `PROJECT_UNBORN` codes to offer repository setup and retry; this recovery path can therefore fail for remote projects. Preserve the structured code when forwarding the error.
- **Parity finding:** Local creation navigates to the new project's orchestrator session after it appears in the refreshed workspace query. This wrapper navigates to the project page after registration, then only invalidates the remote query when the orchestrator starts; it never opens that session. Review whether remote creation should follow the existing local navigation behavior.
- **Status:** Resolved: structured API error codes survive, and the new remote orchestrator opens in the shared session view.

### 9. RemoteHostsSection.tsx

- **Current PR change:** Adds offline/retry and add-project entries for each host, plus host-labeled projects and sessions in the existing sidebar.
- **Architecture finding:** Its `RemoteProjectRow` and `RemoteSessionRow` are separate 300-line presentations of the same project/session tree already rendered by `Sidebar.tsx` (`ProjectItem` and `SessionRow`). This duplicates layout and behavior rather than supplying host-scoped data to the established rows. Concrete gaps include the local session-list cap/reordering and agent-switch status presentation, so future local sidebar refinements can drift further.
- **Agreed direction to apply in the batch:** Keep host-specific identity/offline/add-project affordances, but make the existing sidebar rows accept host-scoped data and actions, then remove the parallel remote row implementations. Preserve the current local appearance and interactions.
- **Status:** Resolved: remote host chrome remains, but projects/sessions now use the existing `ProjectItem` and `SessionRow`; the duplicate row code and tests were removed.

### 10. RestoreUnavailableDialog.tsx

- **Current PR change:** Reuses the existing restore-failure dialog for remote sessions, opening project settings or recreating the orchestrator on the selected host. It changes no layout.
- **Review finding:** It calls both `useWorkspaceScope(session.workspaceId)` (local daemon enabled) and `useRemoteProjectQuery(hostId, session.workspaceId)` for a remote session, although `useWorkspaceScope` already accepts a `hostId` and selects the remote workspace. This needlessly queries the laptop daemon while showing a remote dialog and duplicates source-selection logic. Use the existing host-aware scope hook once.
- **Status:** Resolved: the existing host-aware workspace scope hook supplies both local and remote data.

### 11. ReviewerSelect.tsx

- **Current PR change:** Routes reviewer model catalogs and the Manage harness action to the selected host, with no layout change.
- **Review finding:** `manageAgents` defaults to `true` and its only external use passes `true`; no caller passes `false`. The new prop and conditional menu item add a configuration branch with no actual product use. Remove them unless a real caller requires hiding Manage.
- **Review question:** Replacing `isReadyAgent` with `isLaunchableAgent` also changes the existing local reviewer menu: unknown installation/authentication now remains selectable instead of only known-good agents. This may be a valid readiness policy, but it is not host routing; decide whether that local behavior change belongs in this PR.
- **Status:** Resolved: unused `manageAgents` branch removed; local readiness remains `isReadyAgent`, with remote launchability kept host-specific.

### 12. SessionView.tsx / SessionInspector.tsx — host-scoped UI identity

- **Current PR change:** `SessionView` uses `sessionUiKey(sessionId, hostId)` for inspector state, while routing chat, terminals, files, previews, and interface switching through the selected host. The rail and interface-switch logic were extracted without an intended layout redesign.
- **Review finding:** `SessionView` writes Browser-unseen state under the host-scoped UI key, but `SessionInspector` reads `inspectorSessions[session.id]`. A remote preview can therefore set an unseen badge that the inspector never displays.
- **Review finding:** Several `SessionView` maps for open/dirty file tabs, auxiliary tabs, and file-preview requests still use raw `sessionId`. If two hosts contain the same session ID and the route swaps between them without remounting, UI state can cross hosts. Scope renderer-only state by the host-qualified key while keeping daemon API paths on the raw session ID.
- **Status:** Resolved: inspector, file tabs, and auxiliary renderer state use host-qualified UI session keys while daemon requests use raw session IDs.

### 13. SessionsBoard.tsx — remote board chrome and Cue action

- **Current PR change:** The established Kanban board now reads the selected host's project, sessions, usage, archive, and orchestrator actions. It adds a host label and offline/configuration guidance; the board cards and lanes remain shared.
- **Review finding:** The remote board renders `CueRunMenu` for a Git project, but that menu's cue queries, mutation, terminal cache, and navigation still target the laptop daemon. Running a Cue from a remote board can operate on the wrong machine or fail against a same-ID local project. `ShellTopbar` already gates that same control behind `supportsLocalCues`; hide it on remote boards until Cue operations are host-aware, or route the menu through the selected host.
- **Visual parity finding:** `Boolean(hostId) || usesBoardActionsInPanel()` forces remote board controls into an in-panel header on Windows/Linux, while local boards use the existing ShellTopbar. The shell route separately hides ShellTopbar whenever `hostId` exists. Reuse the platform's existing topbar placement for remote boards, with only the host label/configuration affordances added.
- **Status:** Resolved: remote Cue control is hidden until host-aware, and the existing platform topbar placement is preserved.

### 14. TaskComposer.tsx — remote data routing and preparation cleanup

- **Current PR change:** The existing task composer creates project and standalone tasks on the selected host, reads that host's project/settings/agent readiness/model catalog, and reuses a client request ID for an identical retry. Its visual surface remains shared.
- **Architecture finding:** The composer now selects local versus remote data separately for task creation, project lookup, readiness, and settings. The existing `useSettings` and readiness hooks remain laptop-only while remote equivalents live inline in this UI component. Move host selection into the shared data hooks/client boundary so the composer consumes one host-scoped contract without parallel local/remote query paths.
- **Cleanup bug:** `cancelTaskPreparation` wraps a `void client.DELETE(...)` call in `try/catch`. That catches a synchronous `clientForHost` failure but not an asynchronous rejected DELETE when the remote connection drops, despite the comment promising TTL fallback. Handle the returned promise rejection in this best-effort cleanup.
- **Status:** Resolved: the composer uses shared host-aware settings/readiness hooks, and best-effort preparation DELETE handles promise rejection.

### 15. ChatComposer.tsx — remote attachment reads must fail closed

- **Current PR change:** The shared chat composer accepts a host-specific asset base URL and the daemon's raw session ID, so staged attachment restoration and thumbnails can use the selected host even when local draft storage uses a host-scoped UI key.
- **Review finding:** Both attachment URL sites use `assetBaseUrl ?? getApiBaseUrl()`. `SessionView` passes `baseUrlForHost(hostId)`, which becomes `undefined` on disconnect, so a remote session then forms a laptop-daemon URL with its remote session ID. With a colliding ID/path it can read or display the wrong local attachment instead of failing closed. Keep host identity separate from connection availability, and do not fall back to the local API for remote assets.
- **Status:** Resolved: remote assets fail closed when their host proxy disappears.

### 16. ChatMarkdown.tsx — remote link guard must survive disconnect

- **Current PR change:** The shared Markdown renderer prevents the laptop from opening a remote host's loopback links and images, while retaining external links and supported AO Files links.
- **Review finding:** `ChatWorkspace` passes `remoteHost={Boolean(assetBaseUrl)}`. When a remote host disconnects, `baseUrlForHost` becomes undefined, so the same remote transcript is treated as local and its `localhost` links/images can open against the laptop. Derive remote ownership from `session.hostId`, independently of connection availability, so the guard stays active while offline.
- **Design feedback:** `isHostLocalWebLink` combines the existing loopback helper, a separate 127/8 regex, two unspecified bind addresses, and a React-Markdown IPv6 encoding workaround inside the rendering component. These cases are intentional, but the URL policy should live in one shared host-aware link resolver; keep Markdown responsible for rendering its decision, and preserve the IPv6 and non-`127.0.0.1` cases. This is a preview-location guard, not a security boundary.
- **Status:** Resolved: host ownership is separate from proxy availability; host-local links remain guarded while offline.

### 17. ChatWorkspace.tsx — do not infer local ownership from a missing proxy URL

- **Current PR change:** The existing chat layout is reused. Renderer-only drafts, tab order, minimap, and focus use a host-qualified UI session key; daemon calls and asset paths retain the raw session ID. Remote assets receive a proxy base URL, and remote reviewer/shell tabs render a remote terminal component.
- **Review finding:** `Timeline` and `ChatImageSourceProvider` choose `assetBaseUrl ?? getApiBaseUrl()`. If a remote surface retains cached chat while its proxy URL disappears, image/blob requests can target the laptop daemon. This is the same fail-closed issue as `ChatComposer.tsx`; select resource ownership from the session's host, not URL availability.
- **Review finding:** `ReviewerChatSurface` does not pass a session object to `ChatWorkspace`, so the shared component needs explicit host identity; relying only on `session?.hostId` would miss remote reviewer chats.
- **Review finding:** The reviewer and shell terminal branches choose `RemoteTerminalView` only when both `session.hostId` and `assetBaseUrl` exist; otherwise they mount local `TerminalPane`, whose default mux uses the laptop API base. The primary `SessionView` currently unmounts on disconnect, limiting the normal path, but this shared component should never choose a laptop terminal for a remote session. Render an offline state when the remote transport is unavailable, or use the agreed shared terminal transport boundary.
- **Status:** Resolved: remote chat, reviewer, and shell assets/terminals cannot fall back to the laptop daemon after disconnect.

### 18. SessionChatSurface.tsx — route remote browser links through their host

- **Current PR change:** Conversation data, commands, provider catalogs, settings, skills, attachments, and UI state become host-scoped. The local automatic Browser-opening path is disabled for remote sessions.
- **Review finding:** `useSessionBrowserLink(hostId ? undefined : session, ...)`, `onLinkOpen={hostId ? undefined : openLinkInBrowser}`, and the `if (hostId) return` in the auto-open effect mean a remote chat cannot open agent-supplied links in AO Browser the way local chat does. Remote preview transport already exists in `useBrowserView`; route the preview request and browser action through the selected host instead of disabling the behavior. Keep the host-local URL safety policy when doing so.
- **Status:** Resolved: remote browser links route through the selected host and retain the host-local URL policy.

### 19. usePersistentGutterUtility.ts — remove test-only production guard

- **Current PR change:** Adds a guard for `document.elementFromPoint` being absent during gutter-hover replay; a new test deliberately removes that API.
- **Review finding:** This is unrelated to remote-host routing, and `elementFromPoint` exists in the supported Electron browser. Remove the guard and its dedicated test in the final cleanup unless a supported runtime that lacks the API is identified.
- **Status:** Resolved: the test-only production guard and its dedicated test were removed.

### 20. HarnessSettingsSection.tsx — preserve existing local and cloud layout

- **Current PR change:** A host selector chooses this computer or a connected VM. The existing installer, readiness, probe, and login requests are routed to that host; the sign-in terminal uses a host-specific mux. Local and cloud harness content were split into separate components.
- **Review finding:** The refactor moves the Local/Cloud tabs out of the original sticky search row into a new row even when no remote host is selected. Cloud search also loses the original sticky wrapper. These are visible local/cloud UI changes unrelated to selecting a VM; preserve the prior layout and only add the host selector and remote-specific notice where needed.
- **Status:** Resolved: the existing local/cloud sticky search and tabs remain; the host selector is the only added control.

### 21. RemoteHostsSettings.tsx — make successful Add host lead to a connection

- **Current PR change:** Adds the Remote hosts settings screen: enable switch, saved-host list, add/edit form, and removal confirmation. Main-process IPC probes a host before saving; passwords remain in the main-process credential file rather than the renderer's saved-host list.
- **Review finding:** `save()` persists a reachable host and dispatches a refresh, but `useRemoteHosts.refresh()` returns immediately while the separate enable switch is off. Thus Add host can report success yet show no VM projects until the user finds and turns on that switch. Either enable connections after a successful first add or explicitly present the remaining action in the success state; do not leave the user with a silently saved-but-inactive host.
- **Status:** Resolved: adding the first reachable host enables remote connections and refreshes its projects.

### 22. useRemoteProjectBoardActions.ts — reuse the existing host-aware board action

- **Current PR change:** Adds a separate remote board hook for new task, orchestrator open, clean restart, loading state, errors, and navigation.
- **Review finding:** `useProjectOrchestratorAction` already accepts `hostId`, calls `openRemoteOrchestrator`, uses host-scoped mutation keys, and navigates to the remote session; `ShellTopbar` uses it with `hostId`. `SessionsBoard` instead mounts both hooks and selects the new remote one, duplicating the normal open/new-task/error state and splitting pending state between board and topbar. Use the existing host-aware hook for normal board actions, retaining only the clean-restart behavior that it does not cover, with the required offline guard.
- **Follow-up from shared-code review:** `remote-orchestrator.ts` separately implements the same spawn/resume endpoints as local `spawn-orchestrator.ts` because those functions hard-code `apiClient`. Make the existing low-level operations host-aware at the client boundary instead of maintaining a second behavioral path; preserve their source telemetry, structured spawn errors, and saved-prompt resume notice. Keep the rapid-click dedupe where both board and sidebar need it.
- **Status:** Resolved: the board uses the shared host-aware orchestrator action; duplicate board hook removed, with clean restart retained.

### 23. event-transport.ts / workspace-file-events.ts — choose stream fallback by observed delivery

- **Current PR change:** Connects each remote host's CDC and workspace-file streams through its proxy. A `.trycloudflare.com` address skips SSE and polls every two seconds; every other address attempts SSE and retries it after errors.
- **Review finding:** The address suffix is not a capability test. A quick tunnel that carries SSE still polls, while another proxy that buffers an opened SSE stream can leave workspace and conversation updates stale without switching to polling. Apply the agreed bounded stream-delivery probe and fallback per host; preserve the existing host-qualified query keys and current UI.
- **Status:** Resolved: stream-first probing uses observable ready/heartbeat frames and switches to polling for that connection when delivery fails.

### 24. ui-store.ts / remote project creation — keep the new-task provisioning gate

- **Current PR change:** `requestNewTask(projectId, hostId)` bypasses `provisioningProjectIds` for every remote host. `RemoteAddProjectDialog` registers the project before its orchestrator has started.
- **Review finding:** The local flow blocks New Task during that interval; the remote flow permits it. Track provisioning with a host-qualified project key and clear it on remote orchestrator success or failure, rather than skipping the guard merely because `hostId` is present.
- **Status:** Resolved: provisioning/restart gates use host-qualified project keys, including the remote creation interval.

## Remaining acceptance work

- Re-test the fresh-host bootstrap, logout/reboot, resource headroom, mobile reconnect, and the user journey on the VM when the user is ready.
- Remote clone/GitHub creation errors are validated by the host operation on Continue, not by speculative preflight requests as the user types.

## Technical-debt review disposition

- Removed the duplicate remote board and terminal implementations, reused the shared navigation/orchestrator/data paths, and deleted mock-only, duplicate, and vacuous tests identified in `PR5921-TECH-DEBT-REVIEW.md`.
- Kept explicit host-qualified query keys: the suggested generic key wrapper would alter existing cache/invalidation shapes across unrelated features for little savings.
- Kept the tests for credential/host identity, same-ID isolation, ambiguous retries, and offline fail-closed behavior. A cross-suite fixture rewrite was not needed to remove the useless assertions and would add another abstraction to maintain.
- Kept local notification optimistic-cache behavior distinct from remote host requests; remote read/clear operations now live in data helpers rather than the presentation component.
