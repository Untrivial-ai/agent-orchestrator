# Cloud browser live viewer validation report

Date: 2026-09-23

## Result

Stage 2 is implemented and passes the local protocol, security, recovery,
desktop, end-to-end, race, static-analysis, package, and regression checks
listed below.

One Cloud session now has one shared Chromium. Session browser commands update
the desktop viewer, and desktop pointer and keyboard input update the page seen
by the session browser commands. Frames and controls use the direct browser
stream. They do not use the durable worker request queue.

Hosted-provider validation remains external. No supported hosted provider
configuration was present, so this report makes no remote latency claim.

## Implemented behavior

- A versioned binary JPEG envelope with bounded dimensions, payload size, and
  UTF-8 control validation.
- A browserd CDP viewer controller for screencast, pointer, wheel, keyboard,
  text, navigation, tabs, dialogs, and viewport updates.
- Latest-frame backpressure, deferred final-frame delivery, adaptive quality,
  and bounded input rates.
- User and session-agent control arbitration against the same Chromium profile.
- One-use, short-lived viewer tickets bound to organization, session, scopes,
  and current worker epoch. Only ticket hashes are stored.
- An in-memory control-plane relay with one current viewer and one current
  worker. Frame and input payloads have no durable fallback.
- A reconnecting worker stream that stays idle until a viewer attaches and
  does not start Chromium by itself.
- A process-scoped desktop stream reused across Browser panel remounts, with
  fatal retry, request identifiers, stale-epoch fencing, and frame URL cleanup.
- A canvas surface with frame painting, coordinate scaling, input forwarding,
  ownership state, and translated loading, reconnect, and error states.
- Local Compose compatibility for hosts that have the classic Docker builder
  but no buildx plugin.

## Local end-to-end metrics

`cloud/scripts/test-cloud-browser-viewer.sh --local` passed the complete local
scenario.

| Boundary | Observed |
| --- | ---: |
| Viewer attach to first frame | 1,012 ms |
| User input to visible change | 67 ms |
| Session-agent input to visible change | 57 ms |
| Viewer reconnect | 26 ms |
| Slow-viewer recovery | 40 ms |
| Chromium crash recovery | 747 ms |
| Frame | 1280 x 720, 5,683 bytes |

The scenario also proved lazy Chromium startup, same-process browser reuse,
input replay prevention, reconnect after socket loss, latest-frame behavior for
a slow viewer, and recovery after killing Chromium.

The wider Docker lifecycle passed worker replacement, pause and resume,
control-plane restart, full-stack restart, workspace persistence, and cleanup.

## Native desktop proof

The real Electron app ran with isolated desktop data against an isolated local
Docker control plane. It created a normal Cloud task and attached the Browser
panel to the task's live browser stream.

The native run found and fixed two integration defects that unit-only testing
had not exposed:

1. The session screen always constructed the local browser view even when its
   session carried Cloud metadata. It now activates the Cloud view for Cloud
   sessions and keeps the local view inactive.
2. The cloud surface created a canvas ref but did not attach it to the canvas.
   The ref is now attached, and rapid frames are decoded through a coalescing
   painter so a faster producer cannot starve canvas updates.

After those fixes:

- The live canvas painted at 320 x 893 and reported ready with user control.
- A native pointer click changed the remote button label to `clicked`.
- Native keyboard input changed the remote textbox value and document title to
  `native-typed`.
- A session-side browser fill advanced the viewer frame sequence from 181 to
  271.
- The painted canvas fingerprint changed from `652ff856d0b69bff` to
  `c92a5db147fe85b3`, proving that the native canvas pixels changed.

An unlocked Wayland capture was inspected before commit. It shows the real
fullscreen Electron dev app, the active Cloud session terminal, and the live
Browser inspector together. The browser page visibly contains the desktop
typed value, the verified pointer action, and the session-side status update.
The accompanying 36.2-second recording was also inspected at an interaction
frame.

- Screenshot: `docs/screenshots/pr-5543/cloud-browser-live-viewer.png`
- Recording: `docs/screenshots/pr-5543/cloud-browser-live-viewer.mp4`
- Canvas fingerprint before session-side update: `72f8c3d4`
- Canvas fingerprint after session-side update: `f097387d`

## Regression results

| Scope | Result |
| --- | --- |
| Browserd, relay, ticket, worker stream focused Go tests | Passed |
| Cloud module `go test ./...` | Passed |
| Cloud module `go test -race ./...` | Passed |
| Cloud module `go vet ./...` | Passed |
| Cloud module `go build ./...` | Passed |
| Viewer and session-screen focused frontend tests | 151 passed |
| Full frontend suite | 348 files, 5,284 passed, 7 skipped |
| Frontend typecheck | Passed |
| Production Electron package | Passed |
| Cloud typed client | 24 passed, typecheck and build passed |
| Product UI package | 133 passed, typecheck and build passed |
| Deployment tests | 25 passed |
| Local Compose render and shell syntax | Passed |

The frontend host did not have the `zip` executable required by updater test
fixtures. Those fixtures were run with a temporary 7-Zip-backed compatibility
command outside the repository, then the temporary command was deleted. The
native SQLite module was rebuilt for Node after the Electron validation had
rebuilt it for Electron. These were test-environment repairs, not source
changes.

## Security and recovery checks

- Tickets are single-use, short-lived, scope-limited, and epoch-bound.
- Redeem rejects expired, reused, wrong-session, wrong-organization, and stale
  worker tickets.
- Browser frames and user input are never written to PostgreSQL or worker
  request storage.
- CDP and the browser capability remain inside the worker sandbox.
- Slow readers retain only the latest frame and cannot grow an unbounded queue.
- Stale frame epochs and input sequence replays are ignored.
- Viewer disconnect does not stop the browser or Stage 1 commands.
- Worker and control-plane reconnects mint new stream epochs and resume with a
  replacement full frame.

## Cleanup

The isolated app signed out before shutdown. The Electron app, daemon, worker,
control plane, PostgreSQL container, network, and listeners were stopped. The
real local worker image tag was restored, and all scratch data and temporary
test tooling created by this validation were removed.

## Remaining validation

1. Run the normal application path against one configured hosted provider.
2. Record hosted cold-attach, warm-browser attach, input, reconnect, and crash
   recovery distributions by provider and region.
