package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// previewAPIRequest mirrors the daemon's body for
// POST /api/v1/sessions/{id}/preview. An empty Url asks the daemon to
// autodetect a static entry in the workspace. The CLI keeps its own copy so it
// need not import httpd.
type previewAPIRequest struct {
	Url string `json:"url"`
}

type previewServerStartRequest struct {
	Configuration string `json:"configuration,omitempty"`
}

type previewServerStatusDTO struct {
	SessionID     string    `json:"sessionId"`
	State         string    `json:"state"`
	Configuration string    `json:"configuration,omitempty"`
	TargetKind    string    `json:"targetKind,omitempty"`
	URL           string    `json:"url,omitempty"`
	Port          int       `json:"port,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	Error         string    `json:"error,omitempty"`
	Logs          []string  `json:"logs"`
}

func newPreviewCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview [url]",
		Short: "Open a URL (or the workspace's index.html) in the desktop browser panel for the current session",
		Long: "Open a URL in the desktop browser panel for the current session.\n\n" +
			"With no argument it opens the workspace's static entry point, falling\n" +
			"back to this session's existing preview target when no entry point exists.\n" +
			"A workspace-root-relative Markdown or HTML path opens through AO's isolated\n" +
			"file preview. Use `ao preview start` for a configured dev server and\n" +
			"`ao preview clear` to empty the panel.",
		Example: `  ao preview
  ao preview README.md
  ao preview http://localhost:5173
  ao preview start
  ao preview start web
  ao preview status
  ao preview stop
  ao preview clear`,
		Args: atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			var target string
			if len(args) == 1 {
				target = args[0]
			}
			return ctx.openPreview(cmd.Context(), target)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the desktop browser panel for the current session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.clearPreview(cmd.Context())
		},
	})
	var startJSON bool
	startCmd := &cobra.Command{
		Use:   "start [configuration]",
		Short: "Start a session-owned dev server from .ao/launch.json and open its preview",
		Args:  atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			configuration := ""
			if len(args) == 1 {
				configuration = args[0]
			}
			status, err := ctx.startPreviewServer(cmd.Context(), configuration)
			if err != nil {
				return err
			}
			return writePreviewServerStatus(cmd.OutOrStdout(), status, startJSON)
		},
	}
	startCmd.Flags().BoolVar(&startJSON, "json", false, "print JSON")
	cmd.AddCommand(startCmd)

	var statusJSON bool
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show this session's managed preview server status and recent logs",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := ctx.previewServerStatus(cmd.Context())
			if err != nil {
				return err
			}
			return writePreviewServerStatus(cmd.OutOrStdout(), status, statusJSON)
		},
	}
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "print JSON")
	cmd.AddCommand(statusCmd)

	var stopJSON bool
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop this session's managed preview server",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := ctx.stopPreviewServer(cmd.Context())
			if err != nil {
				return err
			}
			return writePreviewServerStatus(cmd.OutOrStdout(), status, stopJSON)
		},
	}
	stopCmd.Flags().BoolVar(&stopJSON, "json", false, "print JSON")
	cmd.AddCommand(stopCmd)
	return cmd
}

func (c *commandContext) openPreview(ctx context.Context, target string) error {
	path, err := sessionPreviewPath()
	if err != nil {
		return err
	}
	return c.postJSON(ctx, path, previewAPIRequest{Url: target}, nil)
}

// clearPreview empties the desktop browser panel for the current session
// (`ao preview clear`) by deleting the session's stored preview target.
func (c *commandContext) clearPreview(ctx context.Context) error {
	path, err := sessionPreviewPath()
	if err != nil {
		return err
	}
	return c.deleteJSON(ctx, path, nil)
}

func sessionPreviewPath() (string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", usageError{errors.New("ao preview must run inside an AO session (AO_SESSION_ID is not set)")}
	}
	// PathEscape: session ids are already "-"/digit safe, but keep the URL
	// well-formed regardless.
	return "sessions/" + url.PathEscape(sessionID) + "/preview", nil
}

func previewServerPath() (string, error) {
	path, err := sessionPreviewPath()
	if err != nil {
		return "", err
	}
	return path + "/server", nil
}

func (c *commandContext) startPreviewServer(
	ctx context.Context,
	configuration string,
) (previewServerStatusDTO, error) {
	path, err := previewServerPath()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	var out previewServerStatusDTO
	headers, err := previewServerHeaders()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	err = c.doJSONPathWithHeaders(
		ctx,
		http.MethodPost,
		"/api/v1/"+path,
		previewServerStartRequest{Configuration: configuration},
		&out,
		headers,
	)
	return out, err
}

func (c *commandContext) previewServerStatus(ctx context.Context) (previewServerStatusDTO, error) {
	path, err := previewServerPath()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	var out previewServerStatusDTO
	headers, err := previewServerHeaders()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	err = c.doJSONPathWithHeaders(ctx, http.MethodGet, "/api/v1/"+path, nil, &out, headers)
	return out, err
}

func (c *commandContext) stopPreviewServer(ctx context.Context) (previewServerStatusDTO, error) {
	path, err := previewServerPath()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	var out previewServerStatusDTO
	headers, err := previewServerHeaders()
	if err != nil {
		return previewServerStatusDTO{}, err
	}
	err = c.doJSONPathWithHeaders(ctx, http.MethodDelete, "/api/v1/"+path, nil, &out, headers)
	return out, err
}

func previewServerHeaders() (map[string]string, error) {
	capability := strings.TrimSpace(os.Getenv("AO_BROWSER_CAPABILITY"))
	if capability == "" {
		return nil, usageError{errors.New("ao preview server commands require the owning session capability (AO_BROWSER_CAPABILITY is not set)")}
	}
	return map[string]string{browserCapabilityHeader: capability}, nil
}

func writePreviewServerStatus(out io.Writer, status previewServerStatusDTO, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, status)
	}
	summary := status.State
	if status.Configuration != "" {
		summary += " " + status.Configuration
	}
	if status.URL != "" {
		summary += " " + status.URL
	}
	if _, err := fmt.Fprintln(out, summary); err != nil {
		return err
	}
	if status.Error != "" {
		if _, err := fmt.Fprintln(out, "Error:", status.Error); err != nil {
			return err
		}
	}
	for _, line := range status.Logs {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
