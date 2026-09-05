// Single source of truth for the bundled tmux release.
//
// Two places need it and must never drift: build-tmux.mjs (what gets compiled)
// and forge.config.ts's postPackage gate (what the packaged app is verified
// against). A duplicated literal in the gate is what turns a deliberate version
// bump into a packaging failure.
//
// tmux's client/server protocol is not compatible across versions, so this pin
// also decides which running tmux servers a packaged AO can still talk to.
export const BUNDLED_TMUX_VERSION = "3.5a";
export const BUNDLED_TMUX_SHA256 = "16216bd0877170dfcc64157085ba9013610b12b082548c7c9542cc0103198951";
