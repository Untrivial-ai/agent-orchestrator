package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type browserStatusDTO struct {
	SessionID   string    `json:"sessionId"`
	Connected   bool      `json:"connected"`
	ConnectedAt time.Time `json:"connectedAt,omitempty"`
	Transport   string    `json:"transport"`
}

type browserCommandRequestDTO struct {
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Args      map[string]any `json:"args,omitempty"`
}

type browserCommandResponseDTO struct {
	RequestID string         `json:"requestId"`
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Result    map[string]any `json:"result"`
}

type browserScreenshotFileResult struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

const browserCapabilityHeader = "X-AO-Browser-Capability"
const maxBrowserWaitMillis = 55_000
const (
	browserUntrustedBegin = "<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>"
	browserUntrustedEnd   = "<<<END UNTRUSTED EXTERNAL CONTENT>>>"
)

func newBrowserCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Inspect and control this AO session's shared desktop browser",
		Long: "Inspect and control the target-isolated browser owned by the current AO session.\n\n" +
			"The desktop app must be open. Commands operate the same live page the user sees,\n" +
			"including while the Browser panel is hidden.",
		Args: noArgs,
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the structured response as JSON")

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether the desktop browser runtime is connected",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := ctx.browserStatus(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), status)
			}
			state := "disconnected"
			if status.Connected {
				state = "connected"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Browser runtime: %s (%s)\n", state, status.Transport)
			return err
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "open <url>",
		Short: "Open a URL in this session's browser",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "open", map[string]any{"url": args[0]}, jsonOutput)
		},
	})

	var interactiveOnly bool
	snapshot := &cobra.Command{
		Use:   "snapshot",
		Short: "Print a compact accessibility snapshot with actionable element refs",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "snapshot", map[string]any{"interactive": interactiveOnly}, jsonOutput)
		},
	}
	snapshot.Flags().BoolVar(&interactiveOnly, "interactive", false, "include only actionable elements")
	cmd.AddCommand(snapshot)

	var actVerb, actValue string
	var actNth int
	var actNthSet bool
	act := &cobra.Command{
		Use:   "act <instruction>",
		Short: "Resolve an element by description and act on it, snapshotting and retrying automatically",
		Long: "Snapshots, finds the best-matching element for <instruction> against role/name/text\n" +
			"(no LLM call — deterministic matching, same as reading a snapshot yourself), and performs\n" +
			"--action on it (default click). Retries once on a stale reference. If the match is\n" +
			"ambiguous or absent, returns the candidates or snapshot instead of guessing — fall back\n" +
			"to a manual snapshot/click, retry with --nth, or refine the instruction.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actArgs := map[string]any{"instruction": args[0], "action": actVerb}
			if cmd.Flags().Changed("value") {
				actArgs["value"] = actValue
			}
			if actNthSet {
				actArgs["nth"] = actNth
			}
			return ctx.runBrowserAction(cmd, "act", actArgs, jsonOutput)
		},
	}
	act.Flags().StringVar(&actVerb, "action", "click", "verb to perform on the matched element (click, dblclick, focus, hover, fill, type, check, uncheck)")
	act.Flags().StringVar(&actValue, "value", "", "text to fill/type; required when --action is fill or type")
	act.Flags().IntVar(&actNth, "nth", 0, "0-based index to disambiguate when multiple candidates match equally")
	act.PreRunE = func(cmd *cobra.Command, _ []string) error {
		actNthSet = cmd.Flags().Changed("nth")
		return nil
	}
	cmd.AddCommand(act)

	cmd.AddCommand(&cobra.Command{
		Use:   "click <ref>",
		Short: "Click an element reference from the latest snapshot",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "click", map[string]any{"ref": args[0]}, jsonOutput)
		},
	})

	for _, action := range []struct {
		name  string
		short string
	}{
		{name: "dblclick", short: "Double-click an element reference from the latest snapshot"},
		{name: "focus", short: "Focus an element reference from the latest snapshot"},
		{name: "scrollintoview", short: "Scroll an element reference into view"},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:   action.name + " <ref>",
			Short: action.short,
			Args:  exactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return ctx.runBrowserAction(cmd, action.name, map[string]any{"ref": args[0]}, jsonOutput)
			},
		})
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "drag <source-ref> <target-ref>",
		Short: "Drag one element onto another",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "drag", map[string]any{"ref": args[0], "targetRef": args[1]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "fill <ref> <text>",
		Short: "Replace the value of a form control",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "fill", map[string]any{"ref": args[0], "text": args[1]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "type <ref> <text>",
		Short: "Type text at the current cursor position in a form control",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "type", map[string]any{"ref": args[0], "text": args[1]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "press <key>",
		Short: "Press a key or modifier chord such as Enter or Control+A",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "press", map[string]any{"key": args[0]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "hover <ref>",
		Short: "Move the pointer over an element",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "hover", map[string]any{"ref": args[0]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "highlight <ref>",
		Short: "Visually highlight an element without changing page state",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "highlight", map[string]any{"ref": args[0]}, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "unhighlight",
		Short: "Remove the current element highlight",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "unhighlight", nil, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "tabs",
		Short: "List this session's browser tabs",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "tabs", nil, jsonOutput)
		},
	})

	tabCmd := &cobra.Command{
		Use:   "tab",
		Short: "Create, select, or close a browser tab",
		Args:  noArgs,
	}
	tabCmd.AddCommand(&cobra.Command{
		Use:   "new [url]",
		Short: "Open a new tab, optionally navigating to a URL",
		Args:  atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			actionArgs := map[string]any{}
			if len(args) == 1 {
				actionArgs["url"] = args[0]
			}
			return ctx.runBrowserAction(cmd, "tab-new", actionArgs, jsonOutput)
		},
	})
	tabCmd.AddCommand(&cobra.Command{
		Use:   "select <tab-id>",
		Short: "Make a browser tab active",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "tab-select", map[string]any{"tabId": args[0]}, jsonOutput)
		},
	})
	tabCmd.AddCommand(&cobra.Command{
		Use:   "close [tab-id]",
		Short: "Close a browser tab, defaulting to the active tab",
		Args:  atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			actionArgs := map[string]any{}
			if len(args) == 1 {
				actionArgs["tabId"] = args[0]
			}
			return ctx.runBrowserAction(cmd, "tab-close", actionArgs, jsonOutput)
		},
	})
	cmd.AddCommand(tabCmd)

	devtoolsCmd := &cobra.Command{
		Use:   "devtools",
		Short: "Open and control Chromium's DevTools for the active page",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "devtools-open", nil, jsonOutput)
		},
	}
	devtoolsOpen := &cobra.Command{
		Use:   "open",
		Short: "Open the real Chromium DevTools frontend",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "devtools-open", nil, jsonOutput)
		},
	}
	devtoolsCmd.AddCommand(devtoolsOpen)
	devtoolsCmd.AddCommand(&cobra.Command{
		Use:   "close",
		Short: "Close Chromium DevTools",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBrowserAction(cmd, "devtools-close", nil, jsonOutput)
		},
	})
	cmd.AddCommand(devtoolsCmd)

	var scrollAmount int
	scroll := &cobra.Command{
		Use:   "scroll <up|down|left|right>",
		Short: "Scroll the page in one direction",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(
				cmd,
				"scroll",
				map[string]any{"direction": args[0], "amount": scrollAmount},
				jsonOutput,
			)
		},
	}
	scroll.Flags().IntVar(&scrollAmount, "amount", 600, "scroll distance in CSS pixels")
	cmd.AddCommand(scroll)

	cmd.AddCommand(&cobra.Command{
		Use:   "select <ref> <value>",
		Short: "Select an option value",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "select", map[string]any{"ref": args[0], "value": args[1]}, jsonOutput)
		},
	})

	for _, checked := range []bool{true, false} {
		action := "check"
		short := "Check a checkbox or switch"
		if !checked {
			action = "uncheck"
			short = "Uncheck a checkbox or switch"
		}
		cmd.AddCommand(&cobra.Command{
			Use:   action + " <ref>",
			Short: short,
			Args:  exactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return ctx.runBrowserAction(cmd, action, map[string]any{"ref": args[0]}, jsonOutput)
			},
		})
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "get <property> [ref]",
		Short: "Read a page or element property",
		Long:  "Read url, title, or text from the page, or text, value, or checked from an element reference.",
		Args:  rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			actionArgs := map[string]any{"property": args[0]}
			if len(args) == 2 {
				actionArgs["ref"] = args[1]
			}
			return ctx.runBrowserAction(cmd, "get", actionArgs, jsonOutput)
		},
	})

	var waitText, waitTextGone, waitSelector, waitSelectorGone, waitURL string
	var waitMS, waitStableMS, timeoutMS int
	var waitLoad bool
	waitCmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait for page load, DOM stability, time, text, selector, or URL state",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected := 0
			for _, active := range []bool{
				waitText != "",
				waitTextGone != "",
				waitSelector != "",
				waitSelectorGone != "",
				waitURL != "",
				waitLoad,
				waitStableMS > 0,
				waitMS > 0,
			} {
				if active {
					selected++
				}
			}
			if selected != 1 {
				return usageError{errors.New(
					"choose exactly one of --text, --text-gone, --selector, --selector-gone, --url, --load, --dom-stable, or --ms",
				)}
			}
			if timeoutMS < 1 || timeoutMS > maxBrowserWaitMillis {
				return usageError{fmt.Errorf("--timeout must be between 1 and %d milliseconds", maxBrowserWaitMillis)}
			}
			if waitMS > maxBrowserWaitMillis {
				return usageError{fmt.Errorf("--ms must not exceed %d milliseconds", maxBrowserWaitMillis)}
			}
			if waitStableMS > maxBrowserWaitMillis {
				return usageError{fmt.Errorf("--dom-stable must not exceed %d milliseconds", maxBrowserWaitMillis)}
			}
			if waitStableMS > timeoutMS {
				return usageError{errors.New("--timeout must be at least as long as --dom-stable")}
			}
			args := map[string]any{"timeoutMs": timeoutMS}
			switch {
			case waitText != "":
				args["text"] = waitText
			case waitTextGone != "":
				args["textGone"] = waitTextGone
			case waitSelector != "":
				args["selector"] = waitSelector
			case waitSelectorGone != "":
				args["selectorGone"] = waitSelectorGone
			case waitURL != "":
				args["url"] = waitURL
			case waitLoad:
				args["load"] = true
			case waitStableMS > 0:
				args["stableMs"] = waitStableMS
			default:
				args["ms"] = waitMS
			}
			return ctx.runBrowserAction(cmd, "wait", args, jsonOutput)
		},
	}
	waitCmd.Flags().StringVar(&waitText, "text", "", "wait until visible page text contains this value")
	waitCmd.Flags().StringVar(&waitTextGone, "text-gone", "", "wait until visible page text no longer contains this value")
	waitCmd.Flags().StringVar(&waitSelector, "selector", "", "wait until this CSS selector exists")
	waitCmd.Flags().StringVar(&waitSelectorGone, "selector-gone", "", "wait until this CSS selector no longer exists")
	waitCmd.Flags().StringVar(&waitURL, "url", "", "wait until the current URL contains this value")
	waitCmd.Flags().BoolVar(&waitLoad, "load", false, "wait until the current page finishes loading")
	waitCmd.Flags().IntVar(&waitStableMS, "dom-stable", 0, "wait until the DOM has not mutated for this many milliseconds")
	waitCmd.Flags().IntVar(&waitMS, "ms", 0, "wait for a fixed number of milliseconds")
	waitCmd.Flags().IntVar(&timeoutMS, "timeout", 10_000, "condition timeout in milliseconds")
	cmd.AddCommand(waitCmd)

	var screenshotBase64 bool
	screenshot := &cobra.Command{
		Use:   "screenshot [path]",
		Short: "Capture the current page to a PNG file",
		Long: "Capture the current page to a PNG file. JSON output writes the file and returns compact metadata.\n" +
			"To return inline base64 image data instead, omit the path and use --base64 with --json.",
		Args: atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if screenshotBase64 && !jsonOutput {
				return usageError{errors.New("--base64 requires --json")}
			}
			if screenshotBase64 && len(args) != 0 {
				return usageError{errors.New("--base64 cannot be combined with a screenshot path")}
			}
			resp, err := ctx.browserAction(cmd.Context(), "screenshot", nil)
			if err != nil {
				return err
			}
			if screenshotBase64 {
				return writeJSON(cmd.OutOrStdout(), resp)
			}
			path := "ao-browser-" + ctx.deps.Now().Format("20060102-150405.000") + ".png"
			if len(args) == 1 {
				path = args[0]
			}
			return writeBrowserScreenshot(cmd, resp.Result, path, jsonOutput)
		},
	}
	screenshot.Flags().BoolVar(&screenshotBase64, "base64", false, "include inline base64 image data in JSON output (cannot be used with a path)")
	cmd.AddCommand(screenshot)

	var networkDuration int
	networkCmd := &cobra.Command{
		Use:   "network",
		Short: "Temporarily capture sanitized network request metadata",
		Args:  noArgs,
	}
	networkStart := &cobra.Command{
		Use:   "start",
		Short: "Start bounded metadata-only capture on the active tab",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if networkDuration < 1 || networkDuration > 300 {
				return usageError{errors.New("--duration must be between 1 and 300 seconds")}
			}
			return ctx.runBrowserAction(
				cmd,
				"network-start",
				map[string]any{"durationSeconds": networkDuration},
				jsonOutput,
			)
		},
	}
	networkStart.Flags().IntVar(&networkDuration, "duration", 60, "capture duration in seconds (maximum 300)")
	networkCmd.AddCommand(networkStart)
	for _, subcommand := range []struct {
		name  string
		short string
	}{
		{name: "status", short: "Show capture state without enabling it"},
		{name: "list", short: "List captured sanitized request metadata"},
		{name: "stop", short: "Stop capture and list the retained requests"},
		{name: "clear", short: "Clear retained requests without changing capture state"},
	} {
		networkCmd.AddCommand(&cobra.Command{
			Use:   subcommand.name,
			Short: subcommand.short,
			Args:  noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return ctx.runBrowserAction(cmd, "network-"+subcommand.name, nil, jsonOutput)
			},
		})
	}
	cmd.AddCommand(networkCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "frame <ref|main>",
		Short: "Switch automation into a frame or back to the main document",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runBrowserAction(cmd, "frame", map[string]any{"target": args[0]}, jsonOutput)
		},
	})

	dialogCmd := &cobra.Command{Use: "dialog", Short: "Inspect or handle a page dialog", Args: noArgs}
	dialogCmd.AddCommand(&cobra.Command{
		Use:   "accept [text]",
		Short: "Accept a dialog, optionally supplying prompt text",
		Args:  atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			actionArgs := map[string]any{"operation": "accept"}
			if len(args) == 1 {
				actionArgs["text"] = args[0]
			}
			return ctx.runBrowserAction(cmd, "dialog", actionArgs, jsonOutput)
		},
	})
	for _, operation := range []string{"dismiss", "status"} {
		dialogCmd.AddCommand(&cobra.Command{
			Use:   operation,
			Short: operation + " the current page dialog",
			Args:  noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return ctx.runBrowserAction(cmd, "dialog", map[string]any{"operation": operation}, jsonOutput)
			},
		})
	}
	cmd.AddCommand(dialogCmd)

	for _, action := range []string{"console", "errors"} {
		cmd.AddCommand(&cobra.Command{
			Use:   action,
			Short: "Print captured browser " + action,
			Args:  noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return ctx.runBrowserAction(cmd, action, nil, jsonOutput)
			},
		})
	}
	return cmd
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

func rangeArgs(minimum, maximum int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.RangeArgs(minimum, maximum)(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

func currentBrowserIdentity() (string, string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", "", usageError{errors.New("ao browser must run inside an AO session (AO_SESSION_ID is not set)")}
	}
	capability := strings.TrimSpace(os.Getenv("AO_BROWSER_CAPABILITY"))
	if capability == "" {
		return "", "", usageError{errors.New("ao browser requires the owning session capability (AO_BROWSER_CAPABILITY is not set)")}
	}
	return sessionID, capability, nil
}

func (c *commandContext) browserStatus(ctx context.Context) (browserStatusDTO, error) {
	sessionID, capability, err := currentBrowserIdentity()
	if err != nil {
		return browserStatusDTO{}, err
	}
	var out browserStatusDTO
	err = c.doJSONPathWithHeaders(
		ctx,
		http.MethodGet,
		"/api/v1/browser/status?sessionId="+url.QueryEscape(sessionID),
		nil,
		&out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}

func (c *commandContext) browserAction(ctx context.Context, action string, args map[string]any) (browserCommandResponseDTO, error) {
	sessionID, capability, err := currentBrowserIdentity()
	if err != nil {
		return browserCommandResponseDTO{}, err
	}
	var out browserCommandResponseDTO
	err = c.doJSONPathWithHeaders(
		ctx,
		http.MethodPost,
		"/api/v1/browser/commands",
		browserCommandRequestDTO{SessionID: sessionID, Action: action, Args: args},
		&out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}

func (c *commandContext) runBrowserAction(cmd *cobra.Command, action string, args map[string]any, jsonOutput bool) error {
	resp, err := c.browserAction(cmd.Context(), action, args)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), resp)
	}
	return writeBrowserResult(cmd, action, resp.Result)
}

func writeBrowserResult(cmd *cobra.Command, action string, result map[string]any) error {
	if action == "act" {
		return writeBrowserActResult(cmd, result)
	}
	if action == "snapshot" {
		if text, ok := result["text"].(string); ok {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), browserUntrustedText(text))
			return err
		}
	}
	if action == "console" || action == "errors" {
		messages, _ := result["messages"].([]any)
		if len(messages) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "No browser "+action+" captured.")
			return err
		}
		for _, message := range messages {
			if item, ok := message.(map[string]any); ok {
				level, _ := item["level"].(string)
				text, _ := item["message"].(string)
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", level, browserUntrustedText(text)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if action == "get" {
		if value, ok := result["value"].(string); ok {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), browserUntrustedText(value))
			return err
		}
		if value, ok := result["value"]; ok {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), value)
			return err
		}
	}
	if action == "tabs" {
		tabs, _ := result["tabs"].([]any)
		if len(tabs) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "No browser tabs.")
			return err
		}
		var output strings.Builder
		for _, raw := range tabs {
			tab, _ := raw.(map[string]any)
			id, _ := tab["id"].(string)
			title, _ := tab["title"].(string)
			currentURL, _ := tab["url"].(string)
			marker := " "
			if active, _ := tab["active"].(bool); active {
				marker = "*"
			}
			if _, err := fmt.Fprintf(&output, "%s %s\t%s\t%s\n", marker, id, title, currentURL); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(
			cmd.OutOrStdout(),
			browserUntrustedText(strings.TrimSuffix(output.String(), "\n")),
		)
		return err
	}
	if strings.HasPrefix(action, "network-") {
		return writeBrowserNetworkResult(cmd, action, result)
	}
	if currentURL, ok := result["url"].(string); ok && currentURL != "" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), browserUntrustedText(currentURL))
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), "Browser "+action+" completed.")
	return err
}

func browserUntrustedText(value string) string {
	// Page-controlled text must not be able to inject a delimiter that looks
	// like the end of AO's trust boundary. Escape only exact marker collisions;
	// the surrounding fixed markers remain easy for humans and agents to parse.
	value = strings.ReplaceAll(value, browserUntrustedBegin, `\u003c`+browserUntrustedBegin[1:])
	value = strings.ReplaceAll(value, browserUntrustedEnd, `\u003c`+browserUntrustedEnd[1:])
	return browserUntrustedBegin + "\n" + value + "\n" + browserUntrustedEnd
}

func writeBrowserActResult(cmd *cobra.Command, result map[string]any) error {
	outcome, _ := result["outcome"].(string)
	switch outcome {
	case "matched":
		candidate, _ := result["candidate"].(map[string]any)
		role, _ := candidate["role"].(string)
		name, _ := candidate["name"].(string)
		ref, _ := result["resolvedRef"].(string)
		retried, _ := result["retried"].(bool)
		suffix := ""
		if retried {
			suffix = " (after retrying a stale reference)"
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Acted on: %s [ref=%s]%s\n%s\n", role, ref, suffix, browserUntrustedText(name))
		return err
	case "ambiguous":
		candidates, _ := result["candidates"].([]any)
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Ambiguous — multiple elements matched equally well:"); err != nil {
			return err
		}
		for _, raw := range candidates {
			candidate, _ := raw.(map[string]any)
			role, _ := candidate["role"].(string)
			name, _ := candidate["name"].(string)
			ref, _ := candidate["ref"].(string)
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s [ref=%s]\n%s\n", role, ref, browserUntrustedText(name)); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Pick a ref and act/click directly, retry with --nth, or refine the instruction.")
		return err
	case "no-match":
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No element matched that instruction. Latest snapshot:"); err != nil {
			return err
		}
		text, _ := result["snapshot"].(string)
		_, err := fmt.Fprintln(cmd.OutOrStdout(), browserUntrustedText(text))
		return err
	default:
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Browser act completed.")
		return err
	}
}

func writeBrowserNetworkResult(cmd *cobra.Command, action string, result map[string]any) error {
	if action == "network-clear" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Browser network capture cleared.")
		return err
	}
	active, _ := result["active"].(bool)
	state := "inactive"
	if active {
		state = "active"
	}
	tabID, _ := result["tabId"].(string)
	count := numberString(result["requestCount"])
	maxEntries := numberString(result["maxEntries"])
	if count == "" {
		count = "0"
	}
	if maxEntries == "" {
		maxEntries = "200"
	}
	if action == "network-start" || action == "network-status" {
		_, err := fmt.Fprintf(
			cmd.OutOrStdout(),
			"Browser network capture: %s (tab %s, %s/%s requests, metadata only)\n",
			state,
			tabID,
			count,
			maxEntries,
		)
		return err
	}

	requests, _ := result["requests"].([]any)
	if len(requests) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No browser network requests captured.")
		return err
	}
	var output strings.Builder
	for _, raw := range requests {
		request, _ := raw.(map[string]any)
		method, _ := request["method"].(string)
		currentURL, _ := request["url"].(string)
		resourceType, _ := request["resourceType"].(string)
		status := numberString(request["status"])
		if failed, _ := request["failed"].(bool); failed {
			status = "FAILED"
		} else if status == "" {
			status = "PENDING"
		}
		duration := numberString(request["durationMs"])
		if duration != "" {
			duration += "ms"
		} else {
			duration = "-"
		}
		if _, err := fmt.Fprintf(
			&output,
			"%s %s %s %s %s\n",
			method,
			status,
			resourceType,
			duration,
			currentURL,
		); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(
		cmd.OutOrStdout(),
		browserUntrustedText(strings.TrimSuffix(output.String(), "\n")),
	)
	return err
}

func writeBrowserScreenshot(cmd *cobra.Command, result map[string]any, target string, jsonOutput bool) error {
	encoded, _ := result["data"].(string)
	if encoded == "" {
		return errors.New("browser returned an empty screenshot")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode browser screenshot: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing screenshot %s", abs)
		}
		return err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	width := numberInt(result["width"])
	height := numberInt(result["height"])
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), browserScreenshotFileResult{
			Path:   abs,
			Size:   int64(written),
			Width:  width,
			Height: height,
		})
	}
	size := ""
	if width > 0 && height > 0 {
		size = fmt.Sprintf(" (%dx%d)", width, height)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved %s%s\n", abs, size)
	return err
}

func numberInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func numberString(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.Itoa(int(n))
	case int:
		return strconv.Itoa(n)
	default:
		return ""
	}
}
