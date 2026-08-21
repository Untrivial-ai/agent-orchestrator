package iossdk

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DetectionResult represents the outcome of Xcode detection on macOS.
type DetectionResult struct {
	// XcodeDetected indicates whether full Xcode (not just Command Line Tools) is installed.
	XcodeDetected bool
	// CLTOnly indicates whether only Command Line Tools are installed (no full Xcode).
	CLTOnly bool
	// XcodePath is the path output by `xcode-select -p` if it points to Xcode.
	XcodePath string
	// CLTPath is the path output by `xcode-select -p` if it points to Command Line Tools.
	CLTPath string
	// Message is a human-readable status message.
	Message string
}

// DetectXcode runs xcode-select -p and determines whether full Xcode,
// Command Line Tools only, or nothing is installed.
func DetectXcode() *DetectionResult {
	result := &DetectionResult{}

	// Xcode detection is only meaningful on macOS.
	if os.Getenv("GOOS") != "darwin" {
		result.Message = "Xcode detection is only supported on macOS"
		return result
	}

	// Run xcode-select -p to get the active developer directory path.
	cmd := exec.Command("xcode-select", "-p")
	output, err := cmd.Output()
	if err != nil {
		result.Message = fmt.Sprintf("xcode-select failed: %v", err)
		result.XcodeDetected = false
		result.CLTOnly = false
		return result
	}

	path := strings.TrimSpace(string(output))

	// The CLT-only path always resolves to /Library/Developer/CommandLineTools.
	cltPath := "/Library/Developer/CommandLineTools"
	if path == cltPath {
		result.CLTOnly = true
		result.CLTPath = path
		result.XcodeDetected = false
		result.Message = "Command Line Tools are installed, but full Xcode is not"
		return result
	}

	// Check whether the path points to Xcode.app. Full Xcode typically has
	// an active developer directory that resolves under /Applications/Xcode.app/Contents/Developer.
	xcodeAppSubstring := "/Applications/Xcode.app"
	if strings.Contains(path, xcodeAppSubstring) {
		result.XcodeDetected = true
		result.XcodePath = path

		// Attempt to extract the Xcode version via xcodebuild -version.
		version := getXcodeVersion()
		if version != "" {
			result.Message = fmt.Sprintf("Full Xcode is installed (version %s)", version)
		} else {
			result.Message = "Full Xcode is installed (version unknown)"
		}
		return result
	}

	// The path is neither CLT nor clearly pointing to Xcode.app. This can
	// happen when the developer directory has been moved or deleted.
	result.Message = fmt.Sprintf("Unknown developer directory: %s", path)
	result.XcodeDetected = false
	return result
}

// getXcodeVersion runs `xcodebuild -version` and returns the first line of output.
// It returns an empty string on failure (e.g. CLT-only, or xcodebuild not available).
func getXcodeVersion() string {
	cmd := exec.Command("xcodebuild", "-version")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// Summary returns a concise one-line summary suitable for display in UIs or status endpoints.
func (r *DetectionResult) Summary() string {
	if r.XcodeDetected {
		return "Xcode installed"
	}
	if r.CLTOnly {
		return "Command Line Tools only"
	}
	return "Xcode not installed"
}

// IsAvailable reports whether any Xcode-related tooling is available (full Xcode or CLT).
func (r *DetectionResult) IsAvailable() bool {
	return r.XcodeDetected || r.CLTOnly
}

// HasFullXcode reports whether full Xcode (with SDKs, simulators, and IDE) is installed.
func (r *DetectionResult) HasFullXcode() bool {
	return r.XcodeDetected
}

// HasCLTOnly reports whether only Command Line Tools are installed (no full Xcode).
func (r *DetectionResult) HasCLTOnly() bool {
	return r.CLTOnly
}
