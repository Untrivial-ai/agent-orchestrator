# AO browser troubleshooting

Read this reference after an `ao browser` operation fails. Use the narrowest
recovery path and avoid repeating actions whose external outcome is unknown.

## First checks

```bash
ao browser status --json
ao status
```

Confirm that the AO desktop app is open and the terminal belongs to the intended
worker. `AO_SESSION_ID` and its private capability select and authorize the
browser; never print or copy the capability value.

## Desktop runtime unavailable

`BROWSER_RUNTIME_UNAVAILABLE` means the daemon has no connected Electron browser
runtime. Confirm the desktop is open, wait briefly for it to connect, and retry
the status check once. If it remains unavailable, report the exact error. Do not
substitute a different browser and claim it controls the AO panel.

## Daemon unavailable

Use `ao status` to distinguish a stopped daemon from a browser-only failure.
Run `ao start` only when starting AO is within the user's request. Ask before
restarting an already-running environment because it may interrupt workers.

## Target unavailable

`BROWSER_TARGET_UNAVAILABLE` means the expected renderer, tab, or native view no
longer exists. Check status, list tabs, and retry the original non-consequential
high-level command once:

```bash
ao browser tabs --json
ao browser snapshot --interactive
```

If the session terminated, its browser was deliberately destroyed and cannot be
recovered as the same live instance.

## Stale refs, ambiguity, and no match

- `act` automatically re-snapshots and retries one stale ref exactly once.
- After a primitive `STALE_REFERENCE`, take a new interactive snapshot.
- After `ambiguous`, refine the instruction, use a returned ref, or pass `--nth`.
- After `no-match`, confirm the correct tab/frame, wait for rendering, and read
  the returned snapshot. Neither unresolved outcome performs an action.

For a continuously rerendering page, wait for observable stability before
acting:

```bash
ao browser wait --dom-stable 500
ao browser snapshot --interactive
```

## Automation timeout

Inspect the current page and active tab, then wait on a page condition. Retry a
non-consequential operation once if safe. Never blindly repeat a submit,
purchase, send, delete, or other mutation: the first attempt may have succeeded
despite the timeout.

## Profile transition

A profile-active or profile-switching error means AO is waiting for work or
rebuilding views onto another storage partition. Let the transition complete,
confirm the intended profile in the Browser panel, then retry once.

## Tab/automation registry drift

The BrowserViewHost's tab map and the separate automation runtime can disagree
in a long-running session. AO automatically attempts known resynchronization and
safe close fallbacks. If a command still fails:

1. Run `ao browser tabs --json`.
2. Explicitly select the intended tab.
3. Take a fresh snapshot.
4. Retry a non-consequential operation once.
5. Report persistent drift with the error and tab state.

Do not loop a potentially completed mutating action.

## Navigation failure

Confirm that the target is HTTP(S), the hostname/port is correct, the local app
is running, and redirects do not enter a blocked scheme. Inspect `errors`, then
`console`; enable network capture only when those are insufficient.

## Escalation evidence

When recovery fails, report:

- `ao browser status --json` result without secrets
- the exact error code and message
- intended session and logical tab ID
- whether the panel is visible or hidden
- whether a profile switch or termination just occurred
- the last safe verification result
