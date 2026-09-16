# Desktop model search review evidence

Captured directly from the running Electron desktop app in an isolated checkout, with its native preload bridge and local daemon connected. The empty standalone chat uses OpenCode's actual 72-model catalog. No task prompt was sent.

The app runs from `/tmp/ao-5280-desktop` with scratch state under `~/.ao/dev/issue-5280`. The renderer uses `http://localhost:5173` and the daemon uses `http://127.0.0.1:55302`. These captures include the real sidebar, chat tab, and composer.

For the before image, only `TurnSettingsBar.tsx` was temporarily restored to its pre-change source at `109c11ada` in the isolated desktop checkout. The after images and recording use the current branch implementation, including the simplified result-count footer. The final source was restored after the comparison.

## Recording

[Watch or download the desktop recording](desktop-search.mp4).

![Recording from the running desktop app](desktop-search.gif)

The recording shows search narrowing the catalog from 72 models to six results, Arrow Up and Shift+Tab returning to the same query, query refinement, and the empty-results state. The capture script asserted the result counts and both focus transitions.

Screenshots and recording frames come from Electron's `WebContents.capturePage()` API. The short recording preserves the native frame sequence and capture timing. Typing and keyboard navigation use Electron's native input APIs. The MP4 retains the full captured area, padded to even dimensions for playback compatibility; the GIF is scaled for inline review.

## Screenshots

- [Before: desktop model list without search](desktop-before.png)
- [After: real catalog with search and its result count](desktop-catalog.png)
- [After: desktop search narrowed to six models](desktop-filtered.png)

Composition handling is covered by the committed component regression tests. The native recording does not exercise an operating-system input method.
