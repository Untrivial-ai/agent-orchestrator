package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const deviceCapabilityHeader = "X-AO-Device-Capability"

type deviceCapabilityDTO struct {
	Platform  string `json:"platform"`
	Available bool   `json:"available"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type deviceDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	Booted   bool   `json:"booted"`
	Busy     bool   `json:"busy"`
}

type deviceAttachmentDTO struct {
	SessionID string `json:"sessionId"`
	DeviceID  string `json:"deviceId"`
	Platform  string `json:"platform"`
	Name      string `json:"name"`
}

type deviceStatusDTO struct {
	SessionID    string                `json:"sessionId"`
	Capabilities []deviceCapabilityDTO `json:"capabilities"`
	Attachment   *deviceAttachmentDTO  `json:"attachment,omitempty"`
}

type deviceListDTO struct {
	SessionID string                `json:"sessionId"`
	Devices   []deviceDTO           `json:"devices"`
	Errors    []deviceCapabilityDTO `json:"errors,omitempty"`
}

type deviceCommandRequestDTO struct {
	SessionID       string `json:"sessionId"`
	Action          string `json:"action"`
	DeviceID        string `json:"deviceId,omitempty"`
	Platform        string `json:"platform,omitempty"`
	InteractiveOnly bool   `json:"interactiveOnly,omitempty"`
	Ref             string `json:"ref,omitempty"`
	X               *int   `json:"x,omitempty"`
	Y               *int   `json:"y,omitempty"`
	X1              *int   `json:"x1,omitempty"`
	Y1              *int   `json:"y1,omitempty"`
	X2              *int   `json:"x2,omitempty"`
	Y2              *int   `json:"y2,omitempty"`
	Text            string `json:"text,omitempty"`
	Key             string `json:"key,omitempty"`
	Confirmed       bool   `json:"confirmed,omitempty"`
}

type deviceCommandResponseDTO struct {
	SessionID  string               `json:"sessionId"`
	Action     string               `json:"action"`
	Attachment *deviceAttachmentDTO `json:"attachment,omitempty"`
	Result     map[string]any       `json:"result,omitempty"`
}

type deviceSetupDTO struct {
	Platform   string `json:"platform"`
	State      string `json:"state"`
	Stage      string `json:"stage,omitempty"`
	Message    string `json:"message,omitempty"`
	Progress   int    `json:"progress"`
	Error      string `json:"error,omitempty"`
	Cancelable bool   `json:"cancelable"`
	Retryable  bool   `json:"retryable"`
}
type deviceSetupStatusDTO struct {
	SessionID string           `json:"sessionId"`
	Setups    []deviceSetupDTO `json:"setups"`
}
type deviceSetupRequestDTO struct {
	SessionID       string `json:"sessionId"`
	Platform        string `json:"platform"`
	Action          string `json:"action"`
	LicenseAccepted bool   `json:"licenseAccepted,omitempty"`
}
type deviceSetupResponseDTO struct {
	SessionID string         `json:"sessionId"`
	Setup     deviceSetupDTO `json:"setup"`
}

func newDeviceCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	root := &cobra.Command{
		Use: "device", Short: "Inspect and control this AO session's local iOS Simulator or Android Emulator", Args: noArgs,
	}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the structured response as JSON")

	root.AddCommand(&cobra.Command{Use: "status", Short: "Show local device availability and attachment", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := ctx.deviceStatus(cmd.Context())
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), status)
		}
		for _, capability := range status.Capabilities {
			state := "available"
			if !capability.Available {
				state = capability.Code + ": " + capability.Message
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", capability.Platform, state); err != nil {
				return err
			}
		}
		if status.Attachment != nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Attached: %s (%s)\n", status.Attachment.Name, status.Attachment.DeviceID)
		}
		return err
	}})

	root.AddCommand(&cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List installed simulators and emulators", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		inventory, err := ctx.deviceList(cmd.Context())
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), inventory)
		}
		for _, item := range inventory.Devices {
			state := "stopped"
			if item.Booted {
				state = "booted"
			}
			if item.Busy {
				state += ", busy"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", item.ID, item.Platform, item.Name, state); err != nil {
				return err
			}
		}
		for _, problem := range inventory.Errors {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s unavailable: %s\n", problem.Platform, problem.Message); err != nil {
				return err
			}
		}
		return nil
	}})

	root.AddCommand(&cobra.Command{Use: "open <device-id>", Short: "Boot if needed and attach the selected device", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		platform, err := ctx.findDevicePlatform(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: "open", DeviceID: args[0], Platform: platform}, jsonOutput)
	}})

	var screenshotBase64 bool
	screenshot := &cobra.Command{Use: "screenshot [path]", Short: "Capture the attached device screen", Args: atMostOneArg, RunE: func(cmd *cobra.Command, args []string) error {
		response, err := ctx.deviceAction(cmd.Context(), deviceCommandRequestDTO{Action: "screenshot"})
		if err != nil {
			return err
		}
		encoded, _ := response.Result["pngBase64"].(string)
		if encoded == "" {
			return errors.New("device screenshot response did not contain PNG data")
		}
		if screenshotBase64 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), encoded)
			return err
		}
		path := "device-screenshot.png"
		if len(args) == 1 {
			path = args[0]
		}
		bytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("device screenshot response contained invalid PNG data")
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("write screenshot: %w", err)
		}
		if _, err = file.Write(bytes); err != nil {
			_ = file.Close()
			return fmt.Errorf("write screenshot: %w", err)
		}
		if err = file.Close(); err != nil {
			return fmt.Errorf("close screenshot: %w", err)
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"path": path, "size": len(bytes)})
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
		return err
	}}
	screenshot.Flags().BoolVar(&screenshotBase64, "base64", false, "print PNG bytes as base64 instead of writing a file")
	root.AddCommand(screenshot)

	var interactive bool
	tree := &cobra.Command{Use: "ui-tree", Short: "Capture the attached device accessibility tree", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := ctx.deviceAction(cmd.Context(), deviceCommandRequestDTO{Action: "ui-tree", InteractiveOnly: interactive})
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), response)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), deviceUntrustedText(string(encoded)))
		return err
	}}
	tree.Flags().BoolVar(&interactive, "interactive", false, "include only actionable elements")
	root.AddCommand(tree)

	root.AddCommand(&cobra.Command{Use: "tap <ref>|<x> <y>", Short: "Tap a current UI ref or coordinates", Args: rangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		request := deviceCommandRequestDTO{Action: "tap"}
		var err error
		if len(args) == 1 {
			request.Ref = args[0]
		} else {
			request.X, request.Y, err = coordinatePair(args[0], args[1])
		}
		if err != nil {
			return usageError{err}
		}
		return ctx.runDeviceAction(cmd, request, jsonOutput)
	}})

	swipe := &cobra.Command{Use: "swipe <x1> <y1> <x2> <y2>", Short: "Swipe between device coordinates", Args: exactArgs(4), RunE: func(cmd *cobra.Command, args []string) error {
		x1, y1, err := coordinatePair(args[0], args[1])
		if err != nil {
			return usageError{err}
		}
		x2, y2, err := coordinatePair(args[2], args[3])
		if err != nil {
			return usageError{err}
		}
		return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: "swipe", X1: x1, Y1: y1, X2: x2, Y2: y2}, jsonOutput)
	}}
	root.AddCommand(swipe)

	root.AddCommand(&cobra.Command{Use: "fill <ref> <text>", Short: "Replace text in a referenced input", Args: exactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: "fill", Ref: args[0], Text: args[1]}, jsonOutput)
	}})
	root.AddCommand(&cobra.Command{Use: "type <text>", Short: "Type into the focused device input", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: "type", Text: args[0]}, jsonOutput)
	}})
	root.AddCommand(&cobra.Command{Use: "key <key>", Short: "Press Enter or Return", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: "key", Key: args[0]}, jsonOutput)
	}})
	for _, action := range []string{"back", "home", "close"} {
		root.AddCommand(&cobra.Command{Use: action, Short: action + " the attached device", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runDeviceAction(cmd, deviceCommandRequestDTO{Action: action}, jsonOutput)
		}})
	}

	var yes bool
	shutdown := &cobra.Command{Use: "shutdown [device-id]", Short: "Power off a virtual device", Args: atMostOneArg, RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return usageError{errors.New("device shutdown requires --yes")}
		}
		request := deviceCommandRequestDTO{Action: "shutdown", Confirmed: true}
		if len(args) == 1 {
			request.DeviceID = args[0]
			platform, err := ctx.findDevicePlatform(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			request.Platform = platform
		}
		return ctx.runDeviceAction(cmd, request, jsonOutput)
	}}
	shutdown.Flags().BoolVar(&yes, "yes", false, "confirm powering off a device used by other tools")
	root.AddCommand(shutdown)
	root.AddCommand(newDeviceSetupCommand(ctx, &jsonOutput))
	return root
}

func newDeviceSetupCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	setup := &cobra.Command{Use: "setup", Short: "Install and prepare AO-managed virtual-device tools", Args: noArgs}
	setup.AddCommand(&cobra.Command{Use: "status", Short: "Show managed setup progress", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := ctx.deviceSetupStatus(cmd.Context())
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeJSON(cmd.OutOrStdout(), status)
		}
		for _, item := range status.Setups {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%d%%) %s\n", item.Platform, item.State, item.Progress, item.Message); err != nil {
				return err
			}
		}
		return nil
	}})
	for _, action := range []string{"start", "retry", "cancel"} {
		action := action
		var accepted bool
		command := &cobra.Command{Use: action + " <ios|android>", Short: action + " managed platform setup", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "ios" && args[0] != "android" {
				return usageError{errors.New("platform must be ios or android")}
			}
			if action != "cancel" && !accepted {
				return usageError{errors.New("setup requires --accept-license")}
			}
			response, err := ctx.deviceSetupAction(cmd.Context(), deviceSetupRequestDTO{Platform: args[0], Action: action, LicenseAccepted: accepted})
			if err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s setup: %s (%d%%)\n", response.Setup.Platform, response.Setup.State, response.Setup.Progress)
			return err
		}}
		if action != "cancel" {
			command.Flags().BoolVar(&accepted, "accept-license", false, "confirm acceptance of the platform vendor license terms")
		}
		setup.AddCommand(command)
	}
	return setup
}

func currentDeviceIdentity() (string, string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", "", usageError{errors.New("ao device must run inside an AO session (AO_SESSION_ID is not set)")}
	}
	capability := strings.TrimSpace(os.Getenv("AO_DEVICE_CAPABILITY"))
	if capability == "" {
		return "", "", usageError{errors.New("ao device requires AO_DEVICE_CAPABILITY")}
	}
	return sessionID, capability, nil
}

func (c *commandContext) deviceStatus(ctx context.Context) (deviceStatusDTO, error) {
	sessionID, capability, err := currentDeviceIdentity()
	if err != nil {
		return deviceStatusDTO{}, err
	}
	var out deviceStatusDTO
	err = c.doJSONPathWithHeaders(ctx, http.MethodGet, "/api/v1/devices/status?sessionId="+url.QueryEscape(sessionID), nil, &out, map[string]string{deviceCapabilityHeader: capability})
	return out, err
}

func (c *commandContext) deviceList(ctx context.Context) (deviceListDTO, error) {
	sessionID, capability, err := currentDeviceIdentity()
	if err != nil {
		return deviceListDTO{}, err
	}
	var out deviceListDTO
	err = c.doJSONPathWithHeaders(ctx, http.MethodGet, "/api/v1/devices?sessionId="+url.QueryEscape(sessionID), nil, &out, map[string]string{deviceCapabilityHeader: capability})
	return out, err
}

func (c *commandContext) deviceAction(ctx context.Context, request deviceCommandRequestDTO) (deviceCommandResponseDTO, error) {
	sessionID, capability, err := currentDeviceIdentity()
	if err != nil {
		return deviceCommandResponseDTO{}, err
	}
	request.SessionID = sessionID
	var out deviceCommandResponseDTO
	err = c.doJSONPathWithHeaders(ctx, http.MethodPost, "/api/v1/devices/commands", request, &out, map[string]string{deviceCapabilityHeader: capability})
	return out, err
}

func (c *commandContext) deviceSetupStatus(ctx context.Context) (deviceSetupStatusDTO, error) {
	sessionID, capability, err := currentDeviceIdentity()
	if err != nil {
		return deviceSetupStatusDTO{}, err
	}
	var out deviceSetupStatusDTO
	err = c.doJSONPathWithHeaders(ctx, http.MethodGet, "/api/v1/devices/setup?sessionId="+url.QueryEscape(sessionID), nil, &out, map[string]string{deviceCapabilityHeader: capability})
	return out, err
}

func (c *commandContext) deviceSetupAction(ctx context.Context, request deviceSetupRequestDTO) (deviceSetupResponseDTO, error) {
	sessionID, capability, err := currentDeviceIdentity()
	if err != nil {
		return deviceSetupResponseDTO{}, err
	}
	request.SessionID = sessionID
	var out deviceSetupResponseDTO
	err = c.doJSONPathWithHeaders(ctx, http.MethodPost, "/api/v1/devices/setup", request, &out, map[string]string{deviceCapabilityHeader: capability})
	return out, err
}

func (c *commandContext) runDeviceAction(cmd *cobra.Command, request deviceCommandRequestDTO, jsonOutput bool) error {
	response, err := c.deviceAction(cmd.Context(), request)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), response)
	}
	if response.Attachment != nil {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", response.Action, response.Attachment.Name, response.Attachment.DeviceID)
	} else {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Device "+response.Action+" completed.")
	}
	return err
}

func (c *commandContext) findDevicePlatform(ctx context.Context, id string) (string, error) {
	inventory, err := c.deviceList(ctx)
	if err != nil {
		return "", err
	}
	for _, device := range inventory.Devices {
		if device.ID == id {
			return device.Platform, nil
		}
	}
	return "", fmt.Errorf("device %q was not found", id)
}

func coordinatePair(xValue, yValue string) (*int, *int, error) {
	x, err := strconv.Atoi(xValue)
	if err != nil || x < 0 {
		return nil, nil, errors.New("coordinates must be non-negative integers")
	}
	y, err := strconv.Atoi(yValue)
	if err != nil || y < 0 {
		return nil, nil, errors.New("coordinates must be non-negative integers")
	}
	return &x, &y, nil
}

func deviceUntrustedText(value string) string {
	return "<<<BEGIN UNTRUSTED DEVICE CONTENT>>>\n" + strings.ReplaceAll(value, "<<<END UNTRUSTED DEVICE CONTENT>>>", "\\u003c<<END UNTRUSTED DEVICE CONTENT>>>") + "\n<<<END UNTRUSTED DEVICE CONTENT>>>"
}
