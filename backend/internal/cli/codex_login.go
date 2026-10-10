package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/term"
)

const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiDim       = "\x1b[2m"
	ansiCyanBold  = "\x1b[1;36m"
	ansiGreenBold = "\x1b[1;32m"
)

func resolveLoginExecutable(explicit, name string, lookPath func(string) (string, error)) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	resolved, err := lookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s CLI is not installed or is not available on PATH", name)
	}
	return resolved, nil
}

type codexLoginStyle struct {
	enabled bool
}

func newCodexLoginStyle(out io.Writer) codexLoginStyle {
	if os.Getenv("NO_COLOR") != "" {
		return codexLoginStyle{}
	}
	file, ok := out.(*os.File)
	return codexLoginStyle{enabled: ok && term.IsTerminal(file.Fd())}
}

func (s codexLoginStyle) wrap(code, value string) string {
	if !s.enabled {
		return value
	}
	return code + value + ansiReset
}

func (s codexLoginStyle) bold(value string) string {
	return s.wrap(ansiBold, value)
}

func (s codexLoginStyle) dim(value string) string {
	return s.wrap(ansiDim, value)
}

func (s codexLoginStyle) accent(value string) string {
	return s.wrap(ansiCyanBold, value)
}

func (s codexLoginStyle) success(value string) string {
	return s.wrap(ansiGreenBold, value)
}

func readCodexLoginSelection(in io.Reader) (string, error) {
	var value strings.Builder
	var one [1]byte
	for {
		n, err := in.Read(one[:])
		if n == 1 {
			value.WriteByte(one[0])
			if one[0] == '\n' {
				return value.String(), nil
			}
		}
		if err != nil {
			if err == io.EOF && value.Len() > 0 {
				return value.String(), nil
			}
			return "", err
		}
	}
}

func runInteractiveCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec,forbidigo // interactive login must inherit the caller's console.
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func readSecret(in io.Reader) ([]byte, error) {
	if file, ok := in.(*os.File); ok && term.IsTerminal(file.Fd()) {
		return term.ReadPassword(file.Fd())
	}
	value, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	return []byte(value), nil
}
