package iossimulator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BuildApp builds an Xcode project or workspace for the iOS Simulator and
// returns the first .app produced under derivedData.
func BuildApp(project, workspace, scheme, derivedData string) (string, error) {
	if project == "" && workspace == "" || scheme == "" {
		return "", fmt.Errorf("project/workspace and scheme are required")
	}
	if derivedData == "" {
		derivedData = filepath.Join(os.TempDir(), "ao-ios-derived")
	}
	if err := os.RemoveAll(derivedData); err != nil {
		return "", err
	}
	if err := os.MkdirAll(derivedData, 0o750); err != nil {
		return "", err
	}
	args := []string{"xcodebuild"}
	if workspace != "" {
		args = append(args, "-workspace", workspace)
	} else {
		args = append(args, "-project", project)
	}
	args = append(args, "-scheme", scheme, "-sdk", "iphonesimulator", "-configuration", "Debug", "-derivedDataPath", derivedData, "build")
	cmd := exec.Command(args[0], args[1:]...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("xcodebuild: %w: %s", err, output)
	}
	var app string
	_ = filepath.Walk(derivedData, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.IsDir() && filepath.Ext(path) == ".app" && app == "" {
			app = path
		}
		return nil
	})
	if app == "" {
		return "", fmt.Errorf("xcodebuild completed without producing an .app")
	}
	return app, nil
}
