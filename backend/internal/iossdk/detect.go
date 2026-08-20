// Package iossdk contains the macOS-only discovery needed before AO can manage
// an iOS Simulator. It deliberately does not download Xcode: Xcode is an
// Apple-distributed application and must be installed by the user.
package iossdk

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const commandLineToolsPath = "/Library/Developer/CommandLineTools"

// CommandRunner is injectable so detection tests do not need Xcode installed.
type CommandRunner func(name string, args ...string) ([]byte, error)

// DetectionResult describes the active developer directory selected by
// xcode-select.
type DetectionResult struct {
	XcodeDetected bool
	CLTOnly       bool
	XcodePath     string
	CLTPath       string
	Message       string
}

// ToolchainStatus is the aggregate status consumed by the daemon/UI layer.
type ToolchainStatus struct {
	DetectionResult
	SimctlAvailable         bool
	DefaultRuntimeAvailable bool
}

// DetectXcode discovers the active Xcode installation on the current host.
func DetectXcode() DetectionResult {
	return DetectXcodeFor(runtime.GOOS, defaultCommandRunner)
}

// DetectXcodeFor is DetectXcode with explicit host and command dependencies.
// The explicit GOOS argument keeps non-macOS behavior deterministic and makes
// the platform boundary testable without mutating process environment.
func DetectXcodeFor(goos string, run CommandRunner) DetectionResult {
	result := DetectionResult{}
	if goos != "darwin" {
		result.Message = "Xcode detection is only supported on macOS"
		return result
	}
	if run == nil {
		run = defaultCommandRunner
	}

	output, err := run("xcode-select", "-p")
	if err != nil {
		result.Message = fmt.Sprintf("xcode-select failed: %v", err)
		return result
	}
	path := strings.TrimSpace(string(output))
	switch {
	case path == commandLineToolsPath:
		result.CLTOnly = true
		result.CLTPath = path
		result.Message = "Command Line Tools are installed, but full Xcode is not"
	case isXcodeDeveloperDir(path):
		result.XcodeDetected = true
		result.XcodePath = path
		version := xcodeVersion(run)
		if version == "" {
			result.Message = "Full Xcode is installed (version unknown)"
		} else {
			result.Message = fmt.Sprintf("Full Xcode is installed (%s)", version)
		}
	case path == "":
		result.Message = "xcode-select returned an empty developer directory"
	default:
		result.Message = fmt.Sprintf("Unknown developer directory: %s", path)
	}
	return result
}

// DetectToolchain returns Xcode, simctl, and installed-runtime status.
func DetectToolchain() ToolchainStatus {
	return DetectToolchainFor(runtime.GOOS, defaultCommandRunner)
}

// DetectToolchainFor is the injectable form of DetectToolchain.
func DetectToolchainFor(goos string, run CommandRunner) ToolchainStatus {
	status := ToolchainStatus{DetectionResult: DetectXcodeFor(goos, run)}
	if !status.XcodeDetected {
		return status
	}
	if run == nil {
		run = defaultCommandRunner
	}
	if _, err := run("xcrun", "--find", "simctl"); err == nil {
		status.SimctlAvailable = true
	}
	if output, err := run("xcrun", "simctl", "list", "runtimes", "-j"); err == nil {
		status.DefaultRuntimeAvailable = hasAvailableRuntime(output)
	}
	return status
}

func defaultCommandRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func xcodeVersion(run CommandRunner) string {
	output, err := run("xcodebuild", "-version")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Xcode ") {
			return line
		}
	}
	return ""
}

func isXcodeDeveloperDir(path string) bool {
	clean := filepath.Clean(path)
	return strings.HasSuffix(clean, ".app/Contents/Developer") &&
		strings.Contains(filepath.Base(filepath.Dir(filepath.Dir(clean))), "Xcode")
}

func hasAvailableRuntime(output []byte) bool {
	var payload struct {
		Runtimes []struct {
			IsAvailable bool   `json:"isAvailable"`
			Name        string `json:"name"`
		} `json:"runtimes"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return false
	}
	for _, runtime := range payload.Runtimes {
		if runtime.IsAvailable && strings.Contains(strings.ToLower(runtime.Name), "ios") {
			return true
		}
	}
	return false
}
