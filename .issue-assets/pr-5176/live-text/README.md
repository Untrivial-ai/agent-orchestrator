# Immediate text while saving waits — PR 5176

These recordings exercise the actual AO React renderer in Chromium. The Electron bridge, provider text events, and saved-history HTTP responses are simulated. They do not show the packaged Electron app, a real provider call, a measured database latency improvement, or NFS behavior.

- Before implementation: `8343bcc089982e202f0fbc37ff43aa09de3110d4`.
- After implementation: `8bc61ec828f174675437d603da815e7bf32dfb94`.
- Both recordings show the same user prompt and exact reply. After the initial history loads, the fixture holds a saved-history refresh, delivers two text chunks, waits three seconds for visibility in the recording, and releases the saved reply. That deliberate wait exists only in the evidence fixture.
- Before: the response area remains empty while history is held.
- After: the full reply appears while history is still held. Releasing history leaves exactly one copy.
- Assertions inspect the real rendered Conversation log before and after release. `before-held.png` and `after-held.png` capture the held-history state.

The backend boundary is tested separately with the real Chat service, SQLite store, HTTP router, and SSE handler. `TestConversationLiveStreamPrecedesPersistenceAndReconcilesSnapshot` deliberately blocks the first text projection and requires the second text delta to arrive over HTTP SSE before releasing it. `TestLiveTextContinuesWhileProjectionIsBlocked` covers anonymous and explicitly fresh identified events. No production delay is injected by either test.

The production implementation previews fresh text before its persistence queue. Adjacent anonymous deltas are saved together after at most the 50 ms collection window, 16 KiB, or a boundary/stream end. Identified ACP events retain separate durable writes and acknowledgements. The attachment watermark distinguishes new ACP output from replay; replay catchup needs a committed prefix. Initial history and recovery after a journal gap still need a database snapshot. Sustained storage stalls eventually reach the bounded queue's backpressure limit.

## Files and reproduction

`live-display.spec.ts`, `before.config.ts`, and `after.config.ts` are the exact local recording inputs. Their absolute paths identify the two checkouts and the shared installed Playwright/fake-bridge dependency. To replay elsewhere, replace those paths with your checkout locations; use Node 24, install the frontend and product-ui dependencies, and install the Chromium version pinned by the frontend lockfile.

From the after checkout's frontend directory:

```sh
EVIDENCE_MODE=BEFORE EVIDENCE_SHOT=/absolute/evidence/before-held.png npx playwright test --config /absolute/evidence/before.config.ts
EVIDENCE_MODE=AFTER EVIDENCE_SHOT=/absolute/evidence/after-held.png npx playwright test --config /absolute/evidence/after.config.ts
```

The config starts Vite from the selected checkout. The before checkout used the after checkout's frontend/product-ui node_modules via local symlinks. `backend-live-race.log` records the separate focused real-storage regression run; `renderer-after.log` records the after renderer assertion result.

The WebM files are raw Playwright recordings. MP4/GIF versions trim startup to the first evidence banner and preserve playback speed. `recordings.json` documents trimming; no speed-up or concatenation is used. `SHA256SUMS` lists uploaded file hashes. Earlier persistence-count measurements remain separately linked from the PR and keep their original measured commit.
