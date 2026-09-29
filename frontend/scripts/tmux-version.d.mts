// Type declarations for tmux-version.mjs — a plain ESM module (no build step,
// run directly by node in build-tmux.mjs). forge.config.ts is the only *.ts
// file that imports it, and without this it fails typecheck (TS7016).
export declare const BUNDLED_TMUX_VERSION: string;
export declare const BUNDLED_TMUX_SHA256: string;
