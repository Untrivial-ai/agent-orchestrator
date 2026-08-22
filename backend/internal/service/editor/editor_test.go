package editor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The candidate table is long and largely copy-pasted, which is exactly where a
// duplicated id or a bundle path pointing at the wrong app slips in unnoticed.
// Only a handful of these editors are installed on any given machine, so the
// fixture tests below stand up a fake bundle tree and exercise all of them.

func TestCandidateIDsAndCommandsAreUnique(t *testing.T) {
	ids := map[string]bool{}
	commands := map[string]string{}
	for _, c := range candidates {
		if c.id == "" || c.name == "" {
			t.Fatalf("candidate %+v has an empty id or name", c)
		}
		if ids[c.id] {
			t.Fatalf("duplicate editor id %q", c.id)
		}
		ids[c.id] = true
		if len(c.commands) == 0 {
			t.Fatalf("%s has no PATH command to look up", c.id)
		}
		for _, cmd := range c.commands {
			if prev, ok := commands[cmd]; ok {
				t.Fatalf("command %q claimed by both %q and %q", cmd, prev, c.id)
			}
			commands[cmd] = c.id
		}
	}
}

func TestCandidateBundlesAreAppSpecific(t *testing.T) {
	// A bundle left pointing at another editor's .app is the copy-paste bug this
	// table invites, and it would make one editor silently launch another.
	owner := map[string]string{}
	for _, c := range candidates {
		if len(c.apps) == 0 {
			t.Fatalf("%s has no macOS bundle fallback", c.id)
		}
		for _, shim := range c.apps {
			if shim.app == "" || shim.rel == "" {
				t.Fatalf("%s has an incomplete bundle shim %+v", c.id, shim)
			}
			if strings.HasPrefix(shim.rel, "/") {
				t.Fatalf("%s shim %q must be relative to the bundle", c.id, shim.rel)
			}
			if prev, seen := owner[shim.app]; seen && prev != c.id {
				t.Fatalf("%s.app is claimed by both %q and %q", shim.app, prev, c.id)
			}
			owner[shim.app] = c.id
		}
	}
}

func TestVSCodeLeadsThePreferenceOrder(t *testing.T) {
	if candidates[0].id != "vscode" {
		t.Fatalf("first candidate = %q, want vscode (the button is styled after it)", candidates[0].id)
	}
}

// writeShim creates an executable stub where a real editor's launcher would
// be, returning the path actually written. On Windows, editor CLI launchers
// are batch shims (VS Code's own "code.cmd" being the canonical example), so
// the fixture gets a .cmd extension and valid batch content: Go's Windows
// FileMode only reports the executable bit for a handful of recognized
// extensions, and a bare extensionless file is never one of them, so a
// same-content-everywhere fixture would silently fail every detection
// assertion on that platform.
func writeShim(t *testing.T, path string) string {
	t.Helper()
	content := []byte("#!/bin/sh\nexit 0\n")
	if runtime.GOOS == "windows" {
		path += ".cmd"
		content = []byte("@echo off\r\nexit /b 0\r\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// useFixtureRoots isolates detection from the machine's real installs: an empty
// PATH-extras list and a temp application root, so only what a test creates is
// found. Returns the application root.
func useFixtureRoots(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origApps, origPath := appSearchRoots, extraPathDirs
	appSearchRoots, extraPathDirs = []string{root}, nil
	t.Cleanup(func() { appSearchRoots, extraPathDirs = origApps, origPath })
	// exec.LookPath still consults the real PATH; blank it so an editor actually
	// installed on this machine cannot leak into the assertions.
	t.Setenv("PATH", "")
	return root
}

func TestEveryCandidateIsDetectedFromItsAppBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle fallback is macOS-only")
	}
	for _, c := range candidates {
		t.Run(c.id, func(t *testing.T) {
			root := useFixtureRoots(t)
			shim := c.apps[0]
			want := filepath.Join(root, shim.app+".app", shim.rel)
			writeShim(t, want)

			found := Detect()
			if len(found) != 1 {
				t.Fatalf("Detect() = %+v, want exactly %s", found, c.id)
			}
			if found[0].ID != c.id || found[0].Name != c.name {
				t.Fatalf("detected %s/%q, want %s/%q", found[0].ID, found[0].Name, c.id, c.name)
			}
			if found[0].Bin != want {
				t.Fatalf("bin = %q, want %q", found[0].Bin, want)
			}
			// Resolve must reach the same editor when asked for it by id.
			ed, err := Resolve(c.id)
			if err != nil || ed.Bin != want {
				t.Fatalf("Resolve(%q) = %+v, %v", c.id, ed, err)
			}
		})
	}
}

func TestEveryCandidateIsDetectedFromPath(t *testing.T) {
	for _, c := range candidates {
		t.Run(c.id, func(t *testing.T) {
			useFixtureRoots(t)
			bin := t.TempDir()
			extraPathDirs = []string{bin}
			want := writeShim(t, filepath.Join(bin, c.commands[0]))

			found := Detect()
			if len(found) != 1 || found[0].ID != c.id || found[0].Bin != want {
				t.Fatalf("Detect() = %+v, want %s at %q", found, c.id, want)
			}
		})
	}
}

func TestDetectFindsNothingOnACleanMachine(t *testing.T) {
	useFixtureRoots(t)
	if found := Detect(); len(found) != 0 {
		t.Fatalf("Detect() = %+v, want empty when no editor is installed", found)
	}
	if _, err := Resolve(""); err == nil {
		t.Fatal("Resolve accepted a machine with no editors")
	}
}

func TestDetectReturnsPreferenceOrderNotInstallOrder(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle fallback is macOS-only")
	}
	root := useFixtureRoots(t)
	// Install two editors, the lower-priority one first on disk.
	zed, vscode := byID(t, "zed"), byID(t, "vscode")
	writeShim(t, filepath.Join(root, zed.apps[0].app+".app", zed.apps[0].rel))
	writeShim(t, filepath.Join(root, vscode.apps[0].app+".app", vscode.apps[0].rel))

	found := Detect()
	if len(found) != 2 || found[0].ID != "vscode" || found[1].ID != "zed" {
		t.Fatalf("Detect() = %+v, want vscode before zed", found)
	}
	// An empty id takes the preferred editor, which is what the button's main
	// half sends.
	ed, err := Resolve("")
	if err != nil || ed.ID != "vscode" {
		t.Fatalf("Resolve(\"\") = %+v, %v; want vscode", ed, err)
	}
}

func TestDetectIgnoresANonExecutableShim(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bundle fallback is macOS-only")
	}
	root := useFixtureRoots(t)
	vscode := byID(t, "vscode")
	path := filepath.Join(root, vscode.apps[0].app+".app", vscode.apps[0].rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found := Detect(); len(found) != 0 {
		t.Fatalf("Detect() = %+v, want empty for a non-executable shim", found)
	}
}

func TestResolveRejectsAnUnknownEditorID(t *testing.T) {
	if _, err := Resolve("notepad"); err == nil {
		t.Fatal("Resolve accepted an editor AO does not support")
	}
}

func TestResolveRejectsAKnownButUninstalledEditor(t *testing.T) {
	useFixtureRoots(t)
	if _, err := Resolve("goland"); err == nil {
		t.Fatal("Resolve accepted an editor that is not installed")
	}
}

func TestOpenWithoutAResolvedBinaryFails(t *testing.T) {
	if err := Open(Editor{ID: "vscode", Name: "VS Code"}, t.TempDir()); err == nil {
		t.Fatal("Open accepted an editor with no launcher path")
	}
}

func TestOpenPassesTheFolderThenTheFiles(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv.txt")
	bin := writeArgvCapturingEditor(t, filepath.Join(t.TempDir(), "fake-editor"), out)
	file := filepath.Join(dir, "src.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Open(Editor{ID: "fake", Name: "Fake", Bin: bin}, dir, file); err != nil {
		t.Fatal(err)
	}
	argv := readWhenReady(t, out)
	if argv != dir+"\n"+file+"\n" {
		t.Fatalf("argv = %q, want the folder then the file", argv)
	}
}

// writeArgvCapturingEditor writes a fake editor launcher that records its
// arguments, one per line, to out — returning the path Open must be given.
// This exercises launchCommand end to end rather than just detection, so its
// Windows form is a real .cmd batch shim run through cmd.exe (the same shape
// VS Code's own launcher takes), not the placeholder writeShim fixture.
// Positional parameters (%~1, %~2) are used instead of %*: this test always
// calls Open with exactly the folder and one file, and unlike %*, %~N keeps
// an argument containing spaces as one value instead of splitting on them.
func writeArgvCapturingEditor(t *testing.T, path, out string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		path += ".cmd"
		content := "@echo off\r\n(echo %~1\r\necho %~2\r\n) > \"" + out + "\"\r\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	content := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func byID(t *testing.T, id string) candidate {
	t.Helper()
	for _, c := range candidates {
		if c.id == id {
			return c
		}
	}
	t.Fatalf("no candidate %q", id)
	return candidate{}
}

// readWhenReady polls for the launcher's output: Open deliberately does not
// wait on the child, so the file appears shortly after it returns. The budget
// is generous because first-run of a freshly written binary costs a few hundred
// milliseconds on macOS, and more when the whole suite is competing for CPU.
func readWhenReady(t *testing.T, path string) string {
	t.Helper()
	for range 2000 {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		sleepBriefly()
	}
	t.Fatalf("launcher never wrote %s", path)
	return ""
}

func sleepBriefly() { time.Sleep(5 * time.Millisecond) }
