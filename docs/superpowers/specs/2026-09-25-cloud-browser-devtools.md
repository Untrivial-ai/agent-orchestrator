# VM browser DevTools

## Goal

Open the real Chromium Elements, Console and Network interface for the current cloud browser tab. The interface and inspected page both run in the worker. The desktop receives pixels and sends the existing bounded input protocol.

## Design

- Use Chromium's `Target.openDevTools` for the current page, attach a separate CDP session to the returned native inspector target, and load the bundled frontend without docking. A compatibility probe on the local worker's Chromium 153 confirmed opening, rendering, screencasting and closing with networking disabled.
- Reuse the single authenticated viewer stream. DevTools occupies the browser panel while open, with an explicit close action returning to the page. The page stays alive. The active tab and automation target remain the inspected page; the inspector is never listed as a normal tab.
- Add an acknowledged `devtools` control with only `open` and `close` operations. State advertises support and whether DevTools is open. Older workers do not advertise support, so their menu stays disabled. Unsupported Chromium returns an actionable worker-upgrade error.
- Keep navigation and tab controls addressed to the page. Switching or closing the inspected tab closes its inspector. Disconnect/replacement closes the inspector; reconnect returns to the page rather than replaying inspector input.
- Serialize target changes and inspector lifecycle. Tag frames and input with the displayed target, reject stale input after a switch, and block interaction until the matching resized frame is painted.
- Route the existing `ao browser devtools open/close` commands to the attached viewer. Opening without a viewer returns `BROWSER_VIEWER_REQUIRED`; closing is idempotent.

## Security and lifecycle

The debugging socket stays on worker loopback. No CDP endpoint, arbitrary protocol method, expression or inspector URL is accepted from the desktop. Only the worker chooses the native inspector target and fixed bundled frontend URL. Existing viewer tickets, operate permission, worker epochs, acknowledgements, rate limits and ownership arbitration also apply to DevTools. Console execution has the same authority as controlling the inspected page and is unavailable to read-only viewers.

Partial opens close their target. Closing the stream closes the inspector before disposing its CDP connection. The same cleanup runs on tab changes and native inspector closure. The worker does not start Chromium until existing browser intent occurs.

## Verification

1. Worker unit tests: open/close, duplicate commands, partial failure cleanup, unsupported Chromium, stale target input, tab switches, agent ownership and command routing.
2. Relay and renderer tests: read-only/revoked access, allowlisted controls, acknowledgement/error handling, old-worker support gating, target-bound input and reconnect reset.
3. Real worker integration: Elements identifies the fixture page, Console changes that page through viewer keystrokes, Network sees a fixture request, resize remains usable, tabs stay unchanged, and disconnect leaves no inspector target.
4. Cloud build/vet/race suite and frontend checks. Real isolated desktop capture before publication.

## Limits

This implements the existing deferred DevTools feature. Separate background network capture, downloads, local filesystem workspaces and remote-to-local clipboard copying remain outside scope. There is one streamed surface, not simultaneous page and inspector panes. Chromium's experimental native inspector protocol must remain covered by the real-worker test when the image changes.
