# 5. Allow a compatibility-only Unix socket alias outside the AO data directory

Date: 2026-09-03
Status: Accepted

## Context

Historical private tmux sessions keep their socket inode beside `running.json`,
but a configured AO data path can exceed the platform `AF_UNIX` address limit.
Released builds solved that by reaching the canonical socket through a short
directory symlink under the operating system's temporary directory. Rejecting
those paths would strand existing sessions after an update, while moving the
socket itself into temporary storage would make disposable OS state
authoritative.

## Decision

Allow one write outside the configured AO data directory: the tmux adapter may
create an owner-only `/tmp/ao-tmux-<uid>/<hash>` directory symlink that points
to the validated canonical runtime directory. The alias contains no session
data, credentials, or socket inode; it is a derived address translation, is
validated before use, and is safe to delete and recreate.

## Consequences

All authoritative AO state remains under the configured data directory, while
long-path sessions from the historical private-socket release remain
recoverable. No other cache, runtime artifact, or application state inherits
this exception.
