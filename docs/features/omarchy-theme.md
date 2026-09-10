# Omarchy theme inheritance

In Settings → Appearance, choose **Automatic (Omarchy)** as the color theme and
**System** as the appearance. Automatic is the default when no color theme has
been saved. Existing named color themes (including Orchestrate) and explicit
Light/Dark preferences are preserved. Automatic with an explicit appearance
uses Orchestrate in that appearance.

On Linux, Electron main reads `~/.local/state/omarchy/current/theme/colors.toml`,
with `~/.config/omarchy/current/theme/colors.toml` as the legacy location when
the current location is absent. AO checks again every two seconds while the
shell is mounted, following file edits, active symlink replacement, removal,
and recovery. No Omarchy hook or OS configuration change is needed.

The reader accepts only an allowlisted set of quoted six-digit hex colors,
with a 16 KiB limit and regular-file checks. It never evaluates TOML expressions,
CSS, scripts, or commands. Filesystem access stays in Electron main; preload
returns only normalized palette data. Missing, unreadable, incomplete, oversized,
or invalid palettes fall back to AO's existing System/Orchestrate behavior.

Application surfaces use the palette's background, panel/sidebar shades, and
accent through existing tokens. Foregrounds are adjusted when necessary for
readable controls and menus. Terminals receive the palette's background,
foreground, cursor, selection, and ANSI slots through existing xterm plumbing,
without losing scrollback. Palette brightness determines light/dark appearance.
Agent TUIs that draw their own true-color UI and embedded web content retain
control of their own colors; AO does not inject styles into them.

Implementation plan: keep discovery/validation in Electron main; expose a typed
preload read; add Automatic to existing preferences without migrating saved
choices; refresh shared surface/terminal tokens; verify parser failures, active
link replacement, preference precedence, live terminal updates, and menu contrast.
