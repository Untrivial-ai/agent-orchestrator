//go:build windows

package omp

import (
	"context"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// The test executable stands in for a console-subsystem agent binary. A
// background probe must have no console, even when its parent has one.
func TestMain(m *testing.M) {
	if os.Getenv("AO_TEST_BACKGROUND_AGENT_CONSOLE") == "omp" {
		console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if console != 0 {
			fmt.Fprintln(os.Stderr, "background agent command has a console")
			os.Exit(3)
		}
		fmt.Println("99.0.0")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBackgroundCommandHasNoConsole(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_TEST_BACKGROUND_AGENT_CONSOLE", "omp")
	p := &Plugin{resolvedBinary: binary}
	if err := p.requireActivityContract(context.Background()); err != nil {
		t.Fatal(err)
	}
}
