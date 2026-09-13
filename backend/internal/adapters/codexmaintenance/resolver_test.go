package codexmaintenance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type probeFunc func(context.Context, ports.InstallCommand, io.Writer, io.Writer) error

func (f probeFunc) RunInstall(ctx context.Context, cmd ports.InstallCommand, out, stderr io.Writer) error {
	return f(ctx, cmd, out, stderr)
}

type fixture struct {
	r                                 *Resolver
	files, links, replies, tools, env map[string]string
	calls                             []ports.InstallCommand
}

func newFixture(goos, selected string) *fixture {
	f := &fixture{files: map[string]string{}, links: map[string]string{}, replies: map[string]string{}, tools: map[string]string{}, env: map[string]string{}}
	f.r = &Resolver{goos: goos, binary: func(context.Context) (string, error) { return selected, nil }, getenv: func(k string) string { return f.env[k] }}
	f.r.readFile = func(_ context.Context, p string) ([]byte, error) {
		v, ok := f.files[slash(p)]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(v), nil
	}
	f.r.realpath = func(p string) (string, error) {
		p = path.Clean(slash(p))
		if v, ok := f.links[p]; ok {
			return v, nil
		}
		return p, nil
	}
	f.r.lookup = func(p string) (string, error) {
		v, ok := f.tools[p]
		if !ok {
			return "", os.ErrNotExist
		}
		return v, nil
	}
	f.r.runner = probeFunc(func(ctx context.Context, c ports.InstallCommand, out, stderr io.Writer) error {
		f.calls = append(f.calls, c)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		v, ok := f.replies[strings.Join(c.Argv, " ")]
		if !ok {
			return errors.New("unexpected probe: " + strings.Join(c.Argv, " "))
		}
		_, err := io.WriteString(out, v)
		return err
	})
	f.replies[selected+" --version"] = "codex-cli 1.2.3"
	return f
}
func (f *fixture) pkg(root string) {
	f.files[root+"/package.json"] = `{"name":"@openai/codex","bin":{"codex":"bin/codex.js"}}`
	f.files[root+"/bin/codex.js"] = "#!/usr/bin/env node\n"
}

func TestOwnershipAndCommands(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos+"/standalone", func(t *testing.T) {
			selected := "/users/me/bin/codex"
			if goos == "windows" {
				selected = "C:/Users/Me/Codex/bin/codex.exe"
			}
			f := newFixture(goos, selected)
			home := "/custom/shared-codex"
			root := home + "/packages/standalone/releases/1.2.3-target"
			f.links[selected] = root + "/bin/codex"
			f.links[home+"/packages/standalone/current"] = root
			f.replies[selected+" update --help"] = "Usage: codex update"
			f.env["CODEX_HOME"] = "/auth-only-overlay"
			s, err := f.r.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if s.Source != "standalone" || !reflect.DeepEqual(s.Command.Argv, []string{selected, "update"}) {
				t.Fatalf("%+v", s)
			}
			if !strings.Contains(strings.Join(s.Command.Env, "\n"), "CODEX_HOME="+home) {
				t.Fatalf("wrong installation home: %+v", s.Command)
			}
			delete(f.replies, selected+" update --help")
			s, _ = f.r.Resolve(context.Background())
			if len(s.Command.Argv) != 0 {
				t.Fatal("old native CLI must be manual-only")
			}
		})
		t.Run(goos+"/npm", func(t *testing.T) {
			selected, prefix := "/node-version/bin/codex", "/node-version"
			if goos == "windows" {
				selected, prefix = "C:/Users/Me/npm/codex.cmd", "C:/Users/Me/npm"
			}
			f := newFixture(goos, selected)
			root := prefix + "/lib/node_modules/@openai/codex"
			if goos == "windows" {
				root = prefix + "/node_modules/@openai/codex"
				f.files[selected] = "@ECHO off\n\"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*"
			} else {
				f.links[selected] = root + "/bin/codex.js"
			}
			f.pkg(root)
			f.tools["npm"] = "/different/node/bin/npm"
			args := "/different/node/bin/npm root -g --prefix " + prefix
			if goos == "windows" {
				args = "/different/node/bin/npm root -g"
			}
			f.replies[args] = path.Dir(path.Dir(root))
			s, err := f.r.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"/different/node/bin/npm", "install", "-g", "--prefix", prefix, "--allow-scripts=@openai/codex", "@openai/codex@latest"}
			if s.Source != "npm" || !reflect.DeepEqual(s.Command.Argv, want) {
				t.Fatalf("got %+v", s)
			}
			f.replies[args] = "/different/prefix/node_modules"
			s, _ = f.r.Resolve(context.Background())
			if len(s.Command.Argv) != 0 {
				t.Fatal("mismatched prefix authorized")
			}
		})
		for _, manager := range []string{"pnpm", "bun"} {
			t.Run(goos+"/"+manager, func(t *testing.T) {
				base := "/home/me/.local/share/pnpm"
				if manager == "bun" {
					base = "/home/me/.bun"
				}
				if goos == "windows" {
					base = "C:/Users/Me/AppData/Local/pnpm"
					if manager == "bun" {
						base = "C:/Users/Me/.bun"
					}
				}
				bin := base
				if manager == "bun" {
					bin += "/bin"
				}
				selected := bin + "/codex"
				f := newFixture(goos, selected)
				root := base + "/global/5/node_modules"
				if manager == "bun" {
					root = base + "/install/global/node_modules"
				}
				pkg := root + "/@openai/codex"
				f.pkg(pkg)
				f.links[selected] = pkg + "/bin/codex.js"
				f.tools[manager] = "/tools/" + manager
				probe := "/tools/" + manager + " root -g"
				reply := root
				if manager == "bun" {
					probe = "/tools/bun pm -g bin"
					reply = bin
				}
				f.replies[probe] = reply
				s, err := f.r.Resolve(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if s.Source != manager || first(s.Command.Argv) != "/tools/"+manager {
					t.Fatalf("%+v", s)
				}
				f.replies[probe] = "/unrelated"
				s, _ = f.r.Resolve(context.Background())
				if len(s.Command.Argv) != 0 {
					t.Fatal("wrong global root authorized")
				}
			})
		}
	}
}

func TestHomebrewOwnershipAndVersionSource(t *testing.T) {
	for _, kind := range []string{"Cellar", "Caskroom"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture("darwin", "/opt/homebrew/bin/codex")
			resolved := "/opt/homebrew/" + kind + "/codex/1.2.3/bin/codex"
			f.links["/opt/homebrew/bin/codex"] = resolved
			f.tools["brew"] = "/opt/homebrew/bin/brew"
			f.replies["/opt/homebrew/bin/brew --prefix"] = "/opt/homebrew"
			flag := "--formula"
			jsonReply := `{"formulae":[{"name":"codex","versions":{"stable":"1.3.0"}}]}`
			if kind == "Caskroom" {
				flag = "--cask"
				jsonReply = `{"casks":[{"token":"codex","version":"1.3.0,456"}]}`
			}
			f.replies["/opt/homebrew/bin/brew list "+flag+" codex"] = resolved
			f.replies["/opt/homebrew/bin/brew info --json=v2 "+flag+" codex"] = jsonReply
			s, err := f.r.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if s.Source != "homebrew" || s.VersionSource != "homebrew" {
				t.Fatalf("%+v", s)
			}
			latest, err := f.r.Latest(context.Background(), s)
			if err != nil || latest != "1.3.0" {
				t.Fatalf("%q %v", latest, err)
			}
			f.replies["/opt/homebrew/bin/brew --prefix"] = "/other/brew"
			s, _ = f.r.Resolve(context.Background())
			if len(s.Command.Argv) != 0 || s.VersionSource != "homebrew" {
				t.Fatalf("wrong brew or npm fallback: %+v", s)
			}
		})
	}
}

func TestViteDispatcherOwnership(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			root := "/home/.vite-plus"
			selected := root + "/bin/codex"
			tool := root + "/bin/vp"
			if goos == "windows" {
				selected += ".exe"
				tool += ".exe"
			}
			f := newFixture(goos, selected)
			f.tools[path.Base(tool)] = tool
			f.links[tool] = root + "/current/bin/vp"
			f.links[selected] = f.links[tool]
			if goos == "windows" {
				delete(f.links, selected)
				f.files[root+"/bin/codex.shim"] = "vite-plus-shim-v1\nlayout=single\ndata=" + root + "\ncache=" + root + "/cache\n"
			}
			f.replies[tool+" --version"] = "data\t" + root + "\nbin\t" + root + "/bin\ncache\t" + root + "/cache"
			f.files[root+"/bins/codex.json"] = `{"name":"codex","package":"@openai/codex","version":"1.2.3","source":"vp"}`
			f.files[root+"/packages/@openai/codex.json"] = `{"name":"@openai/codex","version":"1.2.3","bins":["codex"],"installId":""}`
			f.pkg(root + "/packages/@openai/codex/lib/node_modules/@openai/codex")
			s, err := f.r.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if s.Source != "vite-plus" || !reflect.DeepEqual(s.Command.Argv, []string{tool, "i", "-g", packageName}) {
				t.Fatalf("%+v", s)
			}
			delete(f.files, root+"/bins/codex.json")
			s, _ = f.r.Resolve(context.Background())
			if len(s.Command.Argv) != 0 {
				t.Fatal("unowned dispatcher authorized")
			}
		})
	}
}

func TestManualAndChangedOwnership(t *testing.T) {
	for _, selected := range []string{"/custom/codex", "/project/node_modules/@openai/codex/bin/codex.js", "/usr/bin/mise"} {
		t.Run(selected, func(t *testing.T) {
			f := newFixture("linux", selected)
			f.tools["npm"] = "/usr/bin/npm"
			f.tools["brew"] = "/usr/bin/brew"
			f.pkg("/project/node_modules/@openai/codex")
			s, err := f.r.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Command.Argv) != 0 || s.Warning == "" {
				t.Fatalf("%+v", s)
			}
			f.links[selected] = "/different/codex"
			next, _ := f.r.Resolve(context.Background())
			if next.Fingerprint == s.Fingerprint {
				t.Fatal("retargeted wrapper retained token")
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestVersionLookupBoundedWithoutNetwork(t *testing.T) {
	for _, body := range []string{`{"version":"1.4.0"}`, `{"version":"garbage"}`, strings.Repeat("x", maxRead+1)} {
		f := newFixture("linux", "/bin/codex")
		f.r.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "registry.npmjs.org" {
				t.Fatal(req.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		v, err := f.r.Latest(context.Background(), ports.CodexInstallation{VersionSource: "npm"})
		if body == `{"version":"1.4.0"}` {
			if err != nil || v != "1.4.0" {
				t.Fatal(v, err)
			}
		} else if err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}

func TestFingerprintTracksUnchangedShimAndReplacedPayload(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "lib", "node_modules", "@openai", "codex")
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(pkg, "package.json"), `{"name":"@openai/codex","bin":{"codex":"bin/codex.js"},"version":"1.0.0"}`)
	entry := filepath.Join(pkg, "bin", "codex.js")
	write(entry, "stable shim")
	before := ExecutableFingerprint(context.Background(), entry)
	write(filepath.Join(pkg, "package.json"), `{"name":"@openai/codex","bin":{"codex":"bin/codex.js"},"version":"1.1.0","new":true}`)
	if before == ExecutableFingerprint(context.Background(), entry) {
		t.Fatal("package update retained stale fingerprint")
	}
}

func TestWindowsPackageShimsAndNativePayload(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"codex.cmd", "@ECHO off\n\"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*"},
		{"codex.ps1", "$basedir=Split-Path $MyInvocation.MyCommand.Definition -Parent\n& node \"$basedir/node_modules/@openai/codex/bin/codex.js\" $args"},
		{"codex", "#!/bin/sh\nbasedir=$(dirname \"$0\")\nexec node \"$basedir/node_modules/@openai/codex/bin/codex.js\" \"$@\""},
		{"node_modules/@openai/codex/node_modules/@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "C:/Users/Some User/npm"
			selected := prefix + "/" + tc.name
			f := newFixture("windows", selected)
			f.files[selected] = tc.script
			f.pkg(prefix + "/node_modules/@openai/codex")
			f.tools[prefix+"/npm.cmd"] = prefix + "/npm.cmd"
			f.replies[prefix+"/npm.cmd root -g"] = prefix + "/node_modules"
			s, err := f.r.Resolve(context.Background())
			if err != nil || s.Source != "npm" || s.Scope != "npm:"+prefix {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestMiseToolCannotMasqueradeAsNpmGlobal(t *testing.T) {
	for _, tool := range []string{"npm-openai-codex", "codex", "node"} {
		prefix := "/home/user/.local/share/mise/installs/" + tool + "/1.2.3"
		selected := prefix + "/bin/codex"
		f := newFixture("linux", selected)
		f.links[selected] = prefix + "/lib/node_modules/@openai/codex/bin/codex.js"
		f.pkg(prefix + "/lib/node_modules/@openai/codex")
		f.tools["npm"] = "/tools/npm"
		f.replies["/tools/npm root -g --prefix "+prefix] = prefix + "/lib/node_modules"
		s, err := f.r.Resolve(context.Background())
		if err != nil || (len(s.Command.Argv) > 0) != (tool == "node") {
			t.Fatalf("%s: %+v %v", tool, s, err)
		}
	}
}

func TestOwnershipProbeCancellationAndBoundedOutput(t *testing.T) {
	f := newFixture("linux", "/selected/codex")
	f.r.runner = probeFunc(func(ctx context.Context, _ ports.InstallCommand, out, _ io.Writer) error {
		_, _ = io.WriteString(out, strings.Repeat("x", maxRead+1))
		return nil
	})
	if _, err := f.r.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("oversized output: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.r.Resolve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe: %v", err)
	}
}

func TestToolLookupHonorsCancellation(t *testing.T) {
	for _, when := range []string{"before", "adjacent_success", "adjacent_failure", "path_success"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r := &Resolver{lookup: func(name string) (string, error) {
				calls++
				if when == "path_success" && calls == 1 {
					return "", os.ErrNotExist
				}
				cancel()
				if when == "adjacent_failure" {
					return "", os.ErrNotExist
				}
				return name, nil
			}}
			wantCalls := 1
			switch when {
			case "before":
				cancel()
				wantCalls = 0
			case "path_success":
				wantCalls = 2
			}
			if got := r.tool(ctx, "npm", "/owning/bin/npm"); got != "" || calls != wantCalls {
				t.Fatalf("canceled lookup = %q after %d lookups, want no tool after %d", got, calls, wantCalls)
			}
		})
	}
}
