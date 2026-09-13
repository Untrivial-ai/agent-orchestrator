package process

import (
	"os"
	"os/exec"
	"path/filepath"
)

const commandLineToolsGit = "/Library/Developer/CommandLineTools/usr/bin/git"

// resolveExecutable keeps AO's Git operations available while a newly
// installed Xcode is waiting for its first-launch license to be accepted.
// Apple's /usr/bin/git follows the active developer directory and exits 69 in
// that state, even when the separately licensed Command Line Tools Git remains
// usable. Preserve any Git selected explicitly through PATH or configuration.
func resolveExecutable(name string) string {
	if filepath.Base(name) != "git" {
		return name
	}

	resolved := name
	if !filepath.IsAbs(resolved) {
		path, err := exec.LookPath(resolved)
		if err != nil {
			return name
		}
		resolved = path
	}
	if filepath.Clean(resolved) != "/usr/bin/git" {
		return name
	}
	if info, err := os.Stat(commandLineToolsGit); err != nil || info.IsDir() {
		return name
	}
	return commandLineToolsGit
}
