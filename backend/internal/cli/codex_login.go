package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

const codexFileStoreOverride = `cli_auth_credentials_store="file"`

const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiDim       = "\x1b[2m"
	ansiCyanBold  = "\x1b[1;36m"
	ansiGreenBold = "\x1b[1;32m"
)

// newCodexLoginCommand is an internal, trusted terminal entry point. It gives
// the user an interactive choice of Codex-supported authentication methods
// while CODEX_HOME points at AO's private pending account slot.
func newCodexLoginCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:    "codex-login",
		Short:  "Sign a managed Codex account in (internal)",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runCodexLogin(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func (c *commandContext) runCodexLogin(ctx context.Context, in io.Reader, out, stderr io.Writer) error {
	codex, err := c.deps.LookPath("codex")
	if err != nil {
		return fmt.Errorf("codex CLI is not installed or is not available on PATH")
	}
	style := newCodexLoginStyle(out)
	if err := writeCodexLoginMenu(out, style); err != nil {
		return err
	}
	selection, err := readCodexLoginSelection(in)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read login method: %w", err)
	}

	args := []string{"-c", codexFileStoreOverride, "login"}
	childInput := in
	switch strings.TrimSpace(selection) {
	case "1":
	case "2":
		args = append(args, "--device-auth")
	case "3", "4":
		flag, label := "--with-api-key", "API key"
		if strings.TrimSpace(selection) == "4" {
			flag, label = "--with-access-token", "access token"
		}
		if _, err := fmt.Fprintf(out, "\n%s\n%s: ", style.dim("Input stays hidden while you type."), style.bold("Enter "+label)); err != nil {
			return err
		}
		secret, err := c.deps.ReadSecret(in)
		if _, writeErr := fmt.Fprintln(out); writeErr != nil {
			return writeErr
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", label, err)
		}
		secret = bytes.TrimSpace(secret)
		if len(secret) == 0 {
			return fmt.Errorf("%s is required", label)
		}
		defer zeroBytes(secret)
		args = append(args, flag)
		payload := append(append([]byte(nil), secret...), '\n')
		defer zeroBytes(payload)
		childInput = bytes.NewReader(payload)
	default:
		return usageError{fmt.Errorf("login method must be 1, 2, 3, or 4")}
	}

	if err := c.deps.RunInteractiveCommand(ctx, codex, args, childInput, out, stderr); err != nil {
		return fmt.Errorf("codex login failed: %w", err)
	}
	if _, err := fmt.Fprintf(out, "\n%s\n", style.success("Codex sign-in complete.")); err != nil {
		return err
	}
	return nil
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

func writeCodexLoginMenu(out io.Writer, style codexLoginStyle) error {
	lines := []string{
		style.accent("Sign in to Codex"),
		style.dim("Choose how you want to authenticate this account."),
		"",
		style.dim("PERSONAL ACCOUNT"),
		fmt.Sprintf("  %s  %s  %s %s", style.accent("1"), style.bold("ChatGPT in browser"), style.success("Recommended"), style.dim("· Continue using your ChatGPT account")),
		fmt.Sprintf("  %s  %s %s", style.accent("2"), style.bold("Device code"), style.dim("· Sign in on another device")),
		"",
		style.dim("DEVELOPER CREDENTIALS"),
		fmt.Sprintf("  %s  %s %s", style.accent("3"), style.bold("OpenAI API key"), style.dim("· Validated and saved by Codex")),
		fmt.Sprintf("  %s  %s %s", style.accent("4"), style.bold("Access token"), style.dim("· For advanced or managed environments")),
		"",
		style.bold("Enter 1-4 and press Return"),
		style.dim("Ctrl+C to cancel · Secret input stays hidden"),
	}
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return err
	}
	_, err := fmt.Fprint(out, style.accent("Selection [1-4]: "))
	return err
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
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
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and argv are resolved and fixed by the internal command.
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
