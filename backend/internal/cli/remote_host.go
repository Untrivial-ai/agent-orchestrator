package cli

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/spf13/cobra"
)

// remoteHostStatus mirrors the small part of the loopback mobile status API
// needed to pair another client with a headless daemon.
type remoteHostStatus struct {
	Enabled      bool   `json:"enabled"`
	LoopbackOnly bool   `json:"loopbackOnly"`
	HostID       string `json:"hostId"`
	Password     string `json:"password"`
	Tunnel       struct {
		Supported bool   `json:"supported"`
		Running   bool   `json:"running"`
		LastError string `json:"lastError"`
	} `json:"tunnel"`
	Endpoints []struct {
		Kind   string `json:"kind"`
		Host   string `json:"host"`
		Port   int    `json:"port"`
		Secure bool   `json:"secure"`
	} `json:"endpoints"`
}

func newRemoteHostCommand(ctx *commandContext) *cobra.Command {
	root := &cobra.Command{Use: "remote-host", Short: "Manage this machine's remote listener"}
	var tunnel, tunnelOnly bool
	for _, action := range []struct {
		name, method, path string
	}{
		{"status", "GET", "mobile/status"},
		{"enable", "POST", "mobile/enable-lan-only"},
		{"disable", "POST", "mobile/disable"},
	} {
		cmd := &cobra.Command{
			Use: action.name, Args: noArgs,
			Short: action.name + " this machine's authenticated remote listener",
			RunE: func(cmd *cobra.Command, _ []string) error {
				var status remoteHostStatus
				method, path := action.method, action.path
				if action.name == "enable" && tunnelOnly {
					method, path = "POST", "mobile/enable-tunnel-only"
				} else if action.name == "enable" && tunnel {
					if err := ctx.doJSON(cmd.Context(), "GET", "mobile/status", nil, &status); err != nil {
						return remoteHostDaemonError(err)
					}
					if status.Enabled && status.LoopbackOnly {
						return errors.New("listener is loopback-only; run `ao remote-host disable` before enabling LAN and tunnel access")
					}
					if !status.Enabled {
						if err := ctx.doJSON(cmd.Context(), "POST", "mobile/enable-lan-only", nil, &status); err != nil {
							return remoteHostDaemonError(err)
						}
					}
					method, path = "POST", "mobile/remote-access"
				}
				err := ctx.doJSON(cmd.Context(), method, path, nil, &status)
				if err != nil {
					return remoteHostDaemonError(err)
				}
				if !status.Enabled {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), "Remote host disabled")
					return err
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Remote host enabled\nHost ID: %s\n", status.HostID); err != nil {
					return err
				}
				publicAddress := false
				for _, endpoint := range status.Endpoints {
					scheme := "http"
					if endpoint.Secure {
						scheme = "https"
					}
					if endpoint.Kind == "tunnel" {
						publicAddress = true
					}
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Address: %s://%s\n", scheme, net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))); err != nil {
						return err
					}
				}
				if (tunnel || tunnelOnly) && action.name == "enable" {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Cloudflare terminates TLS and can see the connection password and traffic. Quick-tunnel URLs change on restart and have no uptime guarantee."); err != nil {
						return err
					}
				}
				if !publicAddress && ((tunnel || tunnelOnly) && action.name == "enable" || status.LoopbackOnly || status.Tunnel.Running || status.Tunnel.LastError != "") {
					message := "Tunnel: starting; run `ao remote-host status` for the HTTPS address"
					if !status.Tunnel.Supported {
						flag := "--tunnel"
						if status.LoopbackOnly || tunnelOnly {
							flag = "--tunnel-only"
						}
						message = "Tunnel unavailable: install cloudflared, then rerun `ao remote-host enable " + flag + "`"
					} else if status.Tunnel.LastError != "" && !status.Tunnel.Running {
						message = "Tunnel unavailable: " + status.Tunnel.LastError
					}
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), message); err != nil {
						return err
					}
				}
				if status.Password != "" {
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "Password: %s\n", status.Password)
					return err
				}
				return nil
			},
		}
		if action.name == "enable" {
			cmd.Flags().BoolVar(&tunnel, "tunnel", false, "Start AO's managed Cloudflare tunnel for access beyond the LAN")
			cmd.Flags().BoolVar(&tunnelOnly, "tunnel-only", false, "Bind only to loopback and start AO's managed Cloudflare tunnel")
			cmd.MarkFlagsMutuallyExclusive("tunnel", "tunnel-only")
		}
		root.AddCommand(cmd)
	}
	return root
}

func remoteHostDaemonError(err error) error {
	if errors.Is(err, errDaemonNotRunning) {
		return daemonUnavailableError{message: "AO daemon is not running — run `ao daemon` on this machine", cause: errDaemonNotRunning}
	}
	return err
}
