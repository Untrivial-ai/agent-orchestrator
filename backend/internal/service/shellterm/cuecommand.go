package shellterm

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// buildCueCommandArgv adds the selected shell's one-shot execution flag while
// keeping the authored command in one argv element. This preserves Unicode,
// whitespace, quotes, pipes, and redirects without AO re-quoting it.
func buildCueCommandArgv(shellArgv []string, command, goos string) ([]string, error) {
	if len(shellArgv) == 0 {
		return nil, apierr.Internal("SHELL_TERMINAL_NO_SHELL", "Could not determine a shell to run the command")
	}
	argv := append([]string(nil), shellArgv...)
	base := strings.ToLower(filepath.Base(argv[0]))
	if goos == "windows" {
		// filepath.Base follows the host OS. Tests and tooling may construct a
		// Windows argv on Unix, so split both Windows separators explicitly.
		if index := strings.LastIndexAny(argv[0], `\/`); index >= 0 {
			base = strings.ToLower(argv[0][index+1:])
		}
	}
	if goos != "windows" {
		return append(argv, "-lc", command), nil
	}
	switch base {
	case "pwsh.exe", "powershell.exe", "pwsh", "powershell":
		return append(argv, "-Command", command), nil
	case "cmd.exe", "cmd":
		return append(argv, "/D", "/S", "/C", command), nil
	case "bash.exe", "bash", "sh.exe", "sh":
		return append(argv, "-c", command), nil
	default:
		return nil, apierr.Invalid("SHELL_TERMINAL_SHELL_UNSUPPORTED",
			fmt.Sprintf("The selected shell cannot run command Cues: %s", argv[0]), nil)
	}
}
