// Package editor detects the external code editors installed on this machine
// and launches one against a directory, optionally focusing specific files.
//
// AO never hands a filesystem path to the renderer (session worktree paths are
// deliberately absent from the wire DTOs), so the daemon both resolves the
// target and spawns the editor. Callers pass an editor ID or take the
// preference order below.
package editor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNoEditor is returned when no supported editor could be found.
var ErrNoEditor = errors.New("no supported editor found")

// ErrUnknownEditor is returned when a caller names an editor AO does not know
// about, or one that is not installed.
var ErrUnknownEditor = errors.New("unknown or unavailable editor")

// Editor is one detected editor, ready to launch.
type Editor struct {
	ID   string
	Name string
	// Bin is the resolved launcher path. Empty on an undetected candidate.
	Bin string
}

// appShim locates a CLI launcher inside a macOS application bundle: the bundle
// name (without ".app") and the launcher's path within it. Kept relative to the
// search roots rather than absolute so tests can point detection at a fixture
// tree and cover every editor, including ones not installed on the machine
// running the suite.
type appShim struct {
	app string
	rel string
}

// candidate is a supported editor and the ways to find its CLI launcher.
type candidate struct {
	id   string
	name string
	// commands are looked up on PATH, first hit wins.
	commands []string
	// apps are the macOS bundle fallbacks used when the CLI shim is not on
	// PATH, which is the common case until the user runs the editor's
	// "install shell command" action.
	apps []appShim
}

// appSearchRoots are the directories scanned for application bundles. A var so
// tests can substitute a fixture tree.
var appSearchRoots = []string{"/Applications", os.ExpandEnv("$HOME/Applications")}

// vscodeForkBundle is the CLI shim path inside a VS Code-derived app bundle.
// Every fork keeps Microsoft's layout, so one helper covers all of them.
func vscodeForkBundle(app, bin string) []appShim {
	return []appShim{{app: app, rel: "Contents/Resources/app/bin/" + bin}}
}

func macBundle(app, rel string) []appShim {
	return []appShim{{app: app, rel: rel}}
}

// candidates is the detection preference order. VS Code leads because the
// button is styled after it; its forks follow so a Cursor-only machine still
// gets a working button without configuration, then Zed, Sublime, and the
// JetBrains IDEs.
//
// Xcode is deliberately absent: its `xed` shim ships with the Command Line
// Tools and fails unless `xcode-select` points at Xcode.app, and Xcode opens
// projects rather than arbitrary folders, so it is a poor fit for a worktree.
//
// Bundle sub-paths confirmed against a real install: VS Code and Cursor
// (Contents/Resources/app/bin/<bin>, the layout every VS Code fork inherits)
// and Zed (Contents/MacOS/cli). The rest follow their vendor's documented
// layout but have not been checked against an installed copy — a wrong
// sub-path degrades to "not detected", never to launching the wrong editor,
// and the PATH lookup still finds them whenever the shell command is present.
var candidates = []candidate{
	{id: "vscode", name: "VS Code", commands: []string{"code"}, apps: vscodeForkBundle("Visual Studio Code", "code")},
	{id: "cursor", name: "Cursor", commands: []string{"cursor"}, apps: vscodeForkBundle("Cursor", "cursor")},
	{id: "windsurf", name: "Windsurf", commands: []string{"windsurf"}, apps: vscodeForkBundle("Windsurf", "windsurf")},
	{id: "zed", name: "Zed", commands: []string{"zed"}, apps: macBundle("Zed", "Contents/MacOS/cli")},
	{id: "trae", name: "Trae", commands: []string{"trae"}, apps: vscodeForkBundle("Trae", "trae")},
	{id: "kiro", name: "Kiro", commands: []string{"kiro"}, apps: vscodeForkBundle("Kiro", "kiro")},
	{id: "positron", name: "Positron", commands: []string{"positron"}, apps: vscodeForkBundle("Positron", "positron")},
	{id: "vscodium", name: "VSCodium", commands: []string{"codium"}, apps: vscodeForkBundle("VSCodium", "codium")},
	{
		id: "vscode-insiders", name: "VS Code Insiders", commands: []string{"code-insiders"},
		apps: vscodeForkBundle("Visual Studio Code - Insiders", "code-insiders"),
	},
	{
		id: "sublime", name: "Sublime Text", commands: []string{"subl"},
		apps: macBundle("Sublime Text", "Contents/SharedSupport/bin/subl"),
	},
	{id: "intellij", name: "IntelliJ IDEA", commands: []string{"idea"}, apps: jetBrainsBundles("IntelliJ IDEA", "idea")},
	{id: "webstorm", name: "WebStorm", commands: []string{"webstorm"}, apps: jetBrainsBundles("WebStorm", "webstorm")},
	{id: "pycharm", name: "PyCharm", commands: []string{"pycharm"}, apps: jetBrainsBundles("PyCharm", "pycharm")},
	{id: "goland", name: "GoLand", commands: []string{"goland"}, apps: jetBrainsBundles("GoLand", "goland")},
	{id: "phpstorm", name: "PhpStorm", commands: []string{"phpstorm"}, apps: jetBrainsBundles("PhpStorm", "phpstorm")},
	{id: "rubymine", name: "RubyMine", commands: []string{"rubymine"}, apps: jetBrainsBundles("RubyMine", "rubymine")},
	{id: "clion", name: "CLion", commands: []string{"clion"}, apps: jetBrainsBundles("CLion", "clion")},
	{id: "rider", name: "Rider", commands: []string{"rider"}, apps: jetBrainsBundles("Rider", "rider")},
	{
		id: "android-studio", name: "Android Studio", commands: []string{"studio"},
		apps: macBundle("Android Studio", "Contents/MacOS/studio"),
	},
	{id: "fleet", name: "Fleet", commands: []string{"fleet"}, apps: macBundle("Fleet", "Contents/MacOS/Fleet")},
}

// jetBrainsBundles covers the two app names Toolbox uses: the plain product
// name, and the edition-suffixed one it installs alongside (PyCharm CE, and
// the "<Product> <year>.<n>" directories older Toolbox builds create).
func jetBrainsBundles(app, bin string) []appShim {
	out := macBundle(app, "Contents/MacOS/"+bin)
	return append(out, macBundle(app+" CE", "Contents/MacOS/"+bin)...)
}

// extraPathDirs are searched after PATH. The daemon is launched by the desktop
// supervisor rather than a login shell, so it can miss the Homebrew and
// /usr/local prefixes where these shims usually live.
//
// JetBrains Toolbox's generated launchers live under the OS-default
// application-data directories (~/Library/Application Support,
// ~/.local/share), which AO's app-state rule forbids reading from. Without an
// explicit exception, a Toolbox-installed IDE is only detected once the user
// adds its scripts directory to PATH themselves.
var extraPathDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/usr/bin",
}

// Detect returns every supported editor found on this machine, in preference
// order. The slice is empty (not nil-checked by callers) when none are found.
func Detect() []Editor {
	found := make([]Editor, 0, len(candidates))
	for _, c := range candidates {
		if bin := c.resolve(); bin != "" {
			found = append(found, Editor{ID: c.id, Name: c.name, Bin: bin})
		}
	}
	return found
}

// Resolve returns the editor to launch. An empty id takes the first detected
// editor in preference order.
func Resolve(id string) (Editor, error) {
	detected := Detect()
	if id == "" {
		if len(detected) == 0 {
			return Editor{}, ErrNoEditor
		}
		return detected[0], nil
	}
	for _, ed := range detected {
		if ed.ID == id {
			return ed, nil
		}
	}
	return Editor{}, ErrUnknownEditor
}

func (c candidate) resolve() string {
	for _, name := range c.commands {
		if bin, err := exec.LookPath(name); err == nil {
			return bin
		}
		for _, dir := range extraPathDirs {
			bin := filepath.Join(dir, name)
			if isExecutableFile(bin) {
				return bin
			}
		}
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, shim := range c.apps {
		for _, root := range appSearchRoots {
			bin := filepath.Join(root, shim.app+".app", shim.rel)
			if isExecutableFile(bin) {
				return bin
			}
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// Open launches the editor on dir, additionally opening each absolute path in
// files as a tab. Every supported editor accepts "<launcher> <folder>
// [file...]" and folds the request into an already-running window.
//
// The child is started and released: these launchers hand off to a running
// instance and exit on their own, and blocking the HTTP handler on an editor
// cold start would stall the request. Only spawn failures (missing or
// non-executable launcher) surface as errors.
func Open(ed Editor, dir string, files ...string) error {
	if ed.Bin == "" {
		return ErrUnknownEditor
	}
	args := append([]string{dir}, files...)
	cmd := launchCommand(ed.Bin, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap so the launcher does not linger as a zombie for the daemon's life.
	go func() { _ = cmd.Wait() }()
	return nil
}
