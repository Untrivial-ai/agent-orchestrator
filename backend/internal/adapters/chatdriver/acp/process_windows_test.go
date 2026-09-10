//go:build windows

package acp

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureProcessGroupHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("node.exe")

	configureProcessGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr = nil, want hidden Windows process attributes")
	}
	if got := cmd.SysProcAttr.CreationFlags; got&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW", got)
	}
	if got := cmd.SysProcAttr.CreationFlags; got&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP", got)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow = false, want true")
	}
}
