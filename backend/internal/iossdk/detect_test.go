package iossdk

import "testing"

func TestDetectXcodeFor(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		commands   map[string][]byte
		errors     map[string]bool
		wantXcode  bool
		wantCLT    bool
		wantStatus string
	}{
		{name: "non mac", goos: "linux", wantStatus: "Xcode detection is only supported on macOS"},
		{name: "clt only", goos: "darwin", commands: map[string][]byte{"xcode-select": []byte(commandLineToolsPath)}, wantCLT: true, wantStatus: "Command Line Tools are installed, but full Xcode is not"},
		{name: "full xcode", goos: "darwin", commands: map[string][]byte{
			"xcode-select": []byte("/Applications/Xcode.app/Contents/Developer\n"),
			"xcodebuild":   []byte("Xcode 16.4\nBuild version 16F6\n"),
		}, wantXcode: true, wantStatus: "Full Xcode is installed (Xcode 16.4)"},
		{name: "missing", goos: "darwin", errors: map[string]bool{"xcode-select": true}, wantStatus: "xcode-select failed: command failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectXcodeFor(tt.goos, func(name string, _ ...string) ([]byte, error) {
				if tt.errors[name] {
					return nil, errCommandFailed{}
				}
				return tt.commands[name], nil
			})
			if got.XcodeDetected != tt.wantXcode || got.CLTOnly != tt.wantCLT || got.Message != tt.wantStatus {
				t.Fatalf("got %+v, want xcode=%v clt=%v message=%q", got, tt.wantXcode, tt.wantCLT, tt.wantStatus)
			}
		})
	}
}

func TestDetectToolchainFor(t *testing.T) {
	got := DetectToolchainFor("darwin", func(name string, _ ...string) ([]byte, error) {
		switch name {
		case "xcode-select":
			return []byte("/Applications/Xcode.app/Contents/Developer"), nil
		case "xcodebuild":
			return []byte("Xcode 16.4\n"), nil
		case "xcrun":
			return []byte(`{"runtimes":[{"name":"iOS 18.5","isAvailable":true}]}`), nil
		default:
			return nil, errCommandFailed{}
		}
	})
	if !got.XcodeDetected || !got.SimctlAvailable || !got.DefaultRuntimeAvailable {
		t.Fatalf("unexpected status: %+v", got)
	}
}

type errCommandFailed struct{}

func (errCommandFailed) Error() string { return "command failed" }
