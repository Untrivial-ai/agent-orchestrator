# Claude Code profiles

Choose a profile in **Project settings → Agents → Claude Code profile**.
AO discovers existing `~/.claude` and initialized `~/.claude-*` directories,
plus an absolute `CLAUDE_CONFIG_DIR` inherited by the daemon. Named directories
must contain Claude settings, configuration, credentials, or a projects directory.
Symlink aliases of the same directory appear once. Discovery reads directory
metadata, never credential contents, and does not execute shell aliases or scripts.

**Use environment default** leaves `CLAUDE_CONFIG_DIR` inherited. **Default**
explicitly clears it, selecting Claude's normal home configuration even when
the daemon inherited another profile. Named profiles set an absolute directory
in the project's `env.CLAUDE_CONFIG_DIR`; other environment settings are preserved.
Existing custom paths remain visible even when discovery cannot find them.

The choice applies to newly launched or restored Claude workers, orchestrators,
and reviewers. Running processes keep their current profile. Changing the profile
does not transfer existing conversations: restoring history from another profile
may fail. AO does not copy credentials, merge profiles, or change Claude's own
authentication precedence for API keys and other environment overrides.

For profiles in arbitrary directories, configure `env.CLAUDE_CONFIG_DIR` through
the existing project configuration API/CLI. Automatic discovery deliberately does
not search the entire filesystem or parse shell configuration.

Claude documents this account separation mechanism in its
[environment variable reference](https://code.claude.com/docs/en/env-vars).
