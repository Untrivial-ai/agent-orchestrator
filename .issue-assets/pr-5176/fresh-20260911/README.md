# Fresh before/after evidence for PR 5176

Recorded 11 September 2026 (Asia/Kolkata), against the PR's actual base and current head:

- Before: `a8df5228460cbc338173bce6bf47770f65193ce6`.
- After: `8bc61ec828f174675437d603da815e7bf32dfb94`.

These are recordings of the actual AO React renderer in Chromium. The Electron bridge, workspace data, provider text events, and saved-history HTTP responses are simulated. The header in each recording identifies this scope. They are not recordings of the packaged Electron app or real provider/NFS measurements.

## What the recordings verify

The same prompt and six text chunks are used in both versions. After initial history loads, the fixture holds the next saved-history response and feeds reply chunks individually. Each chunk is checked with a Playwright assertion before the next arrives.

Before: no reply text appears while the saved-history response is held. After: each cumulative reply prefix appears while that response is still held. Releasing saved history leaves one complete copy in both versions. The after recording then re-delivers the same transient frame and verifies that the reply still appears once.

The full reply is: “The reply appears as each chunk arrives, even while saving is paused.”

Provider pacing (350 ms between asserted chunks) and phase pauses exist only in the recording fixture to make the behavior readable. They are not production delays or latency benchmark results. Default motion is enabled in both recordings. The current timestamps avoid an artificial hours-long working timer.

`before-assertions.json` and `after-assertions.json` record the checked phases and source commits. The PNG files show the held-history and saved states. The WebM files are unedited Playwright video; the MP4/GIF files trim startup only, preserving playback speed. `recordings.json` records the trim offsets.

## Separate real backend regression

`backend-regression.log` is a fresh race-enabled run on the after commit:

```sh
cd backend
go test -race -v ./internal/service/chat ./internal/httpd/controllers -run 'TestLiveTextContinuesWhileProjectionIsBlocked|TestLivePermanentFailureDiscardsQueuedPreviews|TestConversationLiveStreamPrecedesPersistenceAndReconcilesSnapshot' -count=1
```

These tests use the real controller, SQLite store, and HTTP/SSE handler with an injected provider and a deliberately blocked projection. They cover anonymous and explicitly fresh identified text, final reconciliation, and discarding queued previews after permanent storage failure. They passed. This backend test is separate from the simulated renderer recordings.

## Reproduce

Use Node 24 and the Playwright version in the frontend lockfile. Install frontend and product-ui dependencies. `live-display.spec.ts`, `before.config.ts`, and `after.config.ts` are the exact recording inputs; adjust their absolute paths to your checkout and evidence directories. The before checkout shared the after checkout's node_modules through local symlinks, and the fixture served the same installed Geist font files to both renderers.

Run from the after checkout's frontend directory, with `EVIDENCE_MODE` set to BEFORE or AFTER, `EVIDENCE_COMMIT` set to the corresponding hash above, and `EVIDENCE_DIR` set to an absolute output directory:

```sh
npx playwright test --config /absolute/evidence/before.config.ts
npx playwright test --config /absolute/evidence/after.config.ts
```

Each config starts Vite from its selected checkout. Raw videos, screenshots, source, assertion results, and run logs are included. `SHA256SUMS` verifies the uploaded files. Assets are stored separately from the implementation branch.
