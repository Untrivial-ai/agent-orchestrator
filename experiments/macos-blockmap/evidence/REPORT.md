# macOS differential update experiment

Generated: 2026-09-04T15:40:33.599Z

Upstream main: `fccadda53028f5f22dcc64e9410b95c9b47a8b09`

Host: macOS 27.0, arm64, Node v26.7.0

Dependencies: electron-updater 6.8.9, app-builder-lib 26.15.3

## Reconstruction results

| Scenario | Target bytes | HTTP bytes | Bytes saved | Saved | Ranges | Full fallback | ZIP identity |
| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |
| stable-to-stable-arm64 | 34758627 | 6511646 | 28246981 | 81.27% | 4 | no | PASS |
| nightly-to-nightly-arm64 | 34758626 | 15935860 | 18822766 | 54.15% | 3 | no | PASS |
| nightly-to-stable-arm64 | 34758627 | 22196030 | 12562597 | 36.14% | 2 | no | PASS |
| staged-a-then-b-supersedes-arm64 | 34758626 | 15935860 | 18822766 | 54.15% | 3 | no | PASS |
| stable-to-stable-x64 | 34758628 | 6543437 | 28215191 | 81.17% | 4 | no | PASS |
| nightly-to-nightly-x64 | 34758628 | 15999519 | 18759109 | 53.97% | 3 | no | PASS |
| nightly-to-stable-x64 | 34758628 | 22213944 | 12544684 | 36.09% | 2 | no | PASS |
| staged-a-then-b-supersedes-x64 | 34758628 | 15999519 | 18759109 | 53.97% | 3 | no | PASS |
| differential-failure-full-fallback | 34758627 | 34758627 | 0 | 0% | 0 | yes | PASS |
| published-nightly-to-nightly-arm64 | 111107214 | 17275830 | 93831384 | 84.45% | 70 | no | PASS |

All hashes and requested byte ranges are in [evidence.json](./evidence.json) and [requests.jsonl](./requests.jsonl).

## Region sensitivity

| Changed region | Bundled bytes | HTTP bytes | Saved |
| --- | ---: | ---: | ---: |
| Electron Framework | 12582912 | 12565564 | 63.85% |
| ACP runtime/Node/node_modules | 8388608 | 8629173 | 75.17% |
| Go daemon | 6291456 | 6511653 | 81.27% |
| agent-browser | 7340032 | 7546140 | 78.29% |

## Native artifact checks

- ditto extraction: PASS
- symlink preservation: PASS (1)
- AppleDouble entries: 22
- code signature after reconstruction and ditto extraction: PASS
- Gatekeeper assessment: FAIL (exit 3)
- stapled notarization ticket: FAIL (exit 65)
- repeated ditto ZIP byte deterministic with unchanged input metadata: true
- electron-builder-style `zip -q -r -y` identical to ditto ZIP: false

## Decision

NO-GO for restoring macOS blockmaps in product code from this evidence alone. Reconstruction is implementation-grade at the byte-stream layer only. A guarded rollout still requires Developer ID signed and notarized old/new production-shaped artifacts, native Squirrel install and relaunch coverage, and a clean fallback test inside the packaged Electron app. Keep the current full-download baseline until those gates pass.

A future flag should default off, be independently controllable by channel, preserve the cached full previous ZIP and blockmap, log the exact differential failure, verify SHA-512 before handing the ZIP to Squirrel, and immediately perform one clean full download on any blockmap, Range, reconstruction, or digest failure. Stable enablement should follow nightly soak evidence, never precede it.

## Limitations

- Synthetic fixtures use ad-hoc signing. The optional published-artifact run proves Developer ID signature, Gatekeeper, and staple preservation without requiring local credentials.
- The harness validates updater reconstruction, ditto extraction, signature structure, symlinks, and AppleDouble entries, but does not invoke Squirrel ShipIt against /Applications.

## Published signed artifact result

The read-only optional run used `v0.11.2-nightly.202607301613` as old and `v0.11.2-nightly.202608021445` as new. It reconstructed the 111,107,214-byte target with 17,275,830 HTTP bytes across 70 ranges, saving 93,831,384 bytes (84.45%). The ZIP was byte-identical and both the original and reconstructed target passed `codesign --verify --deep --strict`, `spctl -a -vv -t exec`, and `xcrun stapler validate` after ditto extraction. Both preserved 14 symlinks and 65 AppleDouble entries.

The native Squirrel install outcome is existing project evidence, not rerun here: issue #3034 records a PASS updating these exact versions, including ShipIt swap, relaunch, daemon health, codesign, Gatekeeper, and staple validation. This experiment adds differential reconstruction evidence for the same pair.

## Issue mapping

- #3034: isolates the differential reconstruction boundary implicated by the ShipIt failure. AppleDouble entries survive correct reconstruction and ditto extraction.
- #3151: validates why full download remains the safe production baseline and exercises a clean full-download fallback.
- #3267: retains ZIP as the updater artifact. DMG behavior is outside this experiment.
- #3288: uses the pinned 6.8.9 implementation, records exact Range traffic, and applies the documented verification commands.
