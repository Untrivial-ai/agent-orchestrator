# Claude Code profiles

Choose a named Claude profile directly in the agent picker, for example
**Claude Code — personal** or **Claude Code — perforce**. Worker, orchestrator,
and reviewer defaults can use different profiles. A local task can override its
worker default without changing the project. Local profile choices are not
shown for cloud tasks.

## Discovery

AO looks for directories matching `~/.claude*` in the daemon user's home
folder, including names such as `.claude-personal`, `.claude_team`,
`.claude.consulting`, and `.claudeResearch`. It does not search from the filesystem
root or recursively scan the home directory.

Named directories must contain Claude settings, configuration, credentials, or
a projects directory. Files such as `.claude.json` and uninitialized directories
are not profile choices. Discovery checks filesystem metadata without reading
credential contents or executing shell aliases or scripts.

Symlink aliases of the same directory appear once. Named directories take
precedence over aliases: if `~/.claude` points to `~/.claude-perforce`, the picker
shows **Claude Code — perforce**. A distinct `~/.claude` remains the standard
Default profile. An absolute `CLAUDE_CONFIG_DIR` inherited by the daemon is also
included, even when it is outside the naming convention. Saved custom paths
remain selectable when discovery cannot find them.

## Selection and persistence

The provider remains `claude-code`; the selected directory is stored separately
as `agentConfig.claudeConfigDir`. A missing value inherits the applicable
project or role default; an empty string explicitly selects Claude's standard
home configuration. Named profiles use an absolute path.

Existing project `env.CLAUDE_CONFIG_DIR` settings remain a fallback, so upgrading
does not discard an existing profile choice. Other project environment variables
are preserved. Profile choices do not change global Claude configuration or
copy credentials between profiles.

The resolved profile is retained for a session so changing project defaults does
not redirect its restored conversation to another account. Newly created
sessions use the current selection. AO does not start or sign in to a provider
merely to discover profiles, and the daemon's default authentication status does
not prove a named profile is authenticated.

Model menus continue to use AO's project-level model catalog. Selecting a
profile does not create a separate account-specific model catalog or resolve
that account's default model. The profile choice controls the launched process.

Claude's authentication precedence for API keys and other environment overrides
continues to apply. See Claude's
[environment variable reference](https://code.claude.com/docs/en/env-vars).
