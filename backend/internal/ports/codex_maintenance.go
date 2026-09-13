package ports

import "context"

// CodexInstallation is fresh, host-owned evidence about the executable selected
// for Codex launches. Command is never populated from an HTTP request.
type CodexInstallation struct {
	Path, RealPath, Version, Source, Scope, Fingerprint string
	VersionSource, AvailableVersion, Warning            string
	Command                                             InstallCommand
}

// CodexMaintenance resolves ownership separately from optional online advisory
// reads. Resolve must not install anything and must honor cancellation.
type CodexMaintenance interface {
	Resolve(context.Context) (CodexInstallation, error)
	Latest(context.Context, CodexInstallation) (string, error)
}
