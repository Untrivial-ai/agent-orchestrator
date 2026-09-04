package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/devimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type devImportCapture struct {
	method string
	path   string
	body   devImportProjectsRequest
}

func devImportServer(t *testing.T, status int, report devimport.Report) (*httptest.Server, *devImportCapture) {
	t.Helper()
	capture := &devImportCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.method = r.Method
		capture.path = r.URL.Path
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/dev/import-projects" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capture.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(devImportProjectsResponse{Report: report})
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func TestDevImportProjectsDryRunCallsLiveDaemon(t *testing.T) {
	cfg := setConfigEnv(t)
	sourceDir := filepath.Join(t.TempDir(), "source")
	srv, capture := devImportServer(t, http.StatusOK, devimport.Report{
		SourceDataDir: sourceDir,
		TargetDataDir: cfg.dataDir,
		DryRun:        true,
		Inserted:      1,
	})
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "dev", "import-projects", "--from-data-dir", sourceDir, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/dev/import-projects" {
		t.Fatalf("request = %s %s, want POST /api/v1/dev/import-projects", capture.method, capture.path)
	}
	if capture.body.SourceDataDir != sourceDir || !capture.body.DryRun {
		t.Fatalf("request body = %#v", capture.body)
	}
	if !strings.Contains(out, "Dry run -- no changes written.") || !strings.Contains(out, "Inserted: 1") {
		t.Fatalf("out = %q", out)
	}
}

func TestDevImportProjectsRelativeSourceSendsAbsolutePath(t *testing.T) {
	cfg := setConfigEnv(t)
	wd := t.TempDir()
	sourceDir := filepath.Join(wd, "normal-data")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wantSourceDir, err := resolvedPath(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	srv, capture := devImportServer(t, http.StatusOK, devimport.Report{
		SourceDataDir: sourceDir,
		TargetDataDir: cfg.dataDir,
		DryRun:        true,
	})
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "dev", "import-projects", "--from-data-dir", "./normal-data", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.body.SourceDataDir != wantSourceDir {
		t.Fatalf("sourceDataDir = %q, want %q", capture.body.SourceDataDir, wantSourceDir)
	}
	if !filepath.IsAbs(capture.body.SourceDataDir) {
		t.Fatalf("sourceDataDir = %q, want absolute path", capture.body.SourceDataDir)
	}
}

func TestDevImportProjectsHelpDescribesDaemonBackedImport(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "dev", "import-projects", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "target daemon must be running") || !strings.Contains(out, "--dry-run") {
		t.Fatalf("help missing daemon-backed import guidance:\n%s", out)
	}
	if strings.Contains(out, "must be stopped") {
		t.Fatalf("help still says daemon must be stopped:\n%s", out)
	}
}

func TestDevImportProjectsJSON(t *testing.T) {
	cfg := setConfigEnv(t)
	sourceDir := filepath.Join(t.TempDir(), "source")
	srv, _ := devImportServer(t, http.StatusOK, devimport.Report{
		SourceDataDir: sourceDir,
		TargetDataDir: cfg.dataDir,
		Inserted:      1,
	})
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "dev", "import-projects", "--from-data-dir", sourceDir, "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var rep devimport.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report %q: %v", out, err)
	}
	if rep.SourceDataDir != sourceDir || rep.Inserted != 1 || rep.Updated != 0 || rep.Skipped != 0 {
		t.Fatalf("report = %#v", rep)
	}
}

func TestDevImportProjectsDaemonError(t *testing.T) {
	cfg := setConfigEnv(t)
	sourceDir := filepath.Join(t.TempDir(), "source")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"open source store: file does not exist","code":"INTERNAL"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "dev", "import-projects", "--from-data-dir", sourceDir, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "open source store") {
		t.Fatalf("err = %v, want daemon source open failure", err)
	}
}

// `ao dev import-projects --url` resolves BOTH data dirs against the local
// filesystem and then POSTs the local source path for the remote daemon to open
// on its own disk. The real assertion is the empty request log: /api/v1/dev is
// on the LAN block list today, so a request that escapes this guard comes back
// ROUTE_NOT_FOUND and looks, from the exit status alone, exactly like a refusal.
//
// The second case is the ordering proof. Pointing --from-data-dir at the target
// data dir is the input that trips the local same-dir check; if any path
// resolution had run before the guard, that is the error we would see instead.
func TestDevImportProjectsRefusesRemoteTarget(t *testing.T) {
	for _, tc := range []struct {
		name              string
		sourceIsTheTarget bool
	}{
		{name: "default source"},
		{name: "source equal to target", sourceIsTheTarget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aoHome(t)
			cfg := setConfigEnv(t)
			t.Setenv("AO_TOKEN", "tok")
			var args []string
			if tc.sourceIsTheTarget {
				args = []string{"--from-data-dir", cfg.dataDir}
			}

			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)

			_, _, err := executeCLI(t, Deps{}, append([]string{"dev", "import-projects", "--url", srv.URL}, args...)...)
			if err == nil {
				t.Fatal("dev import-projects --url succeeded, want a refusal")
			}
			if requests != 0 {
				t.Fatalf("refused dev import-projects still contacted the remote daemon (%d requests)", requests)
			}
			if code := ExitCode(err); code != 2 {
				t.Errorf("exit code = %d, want 2 (usage)", code)
			}
			if strings.Contains(err.Error(), "source and target data dirs are the same") {
				t.Errorf("local path resolution ran before the remote refusal: %v", err)
			}
			for _, want := range []string{"--url", srv.URL, "ao dev import-projects"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

func TestDevImportProjectsRefusesSameSourceAndTargetDataDir(t *testing.T) {
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3002, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "dev", "import-projects", "--from-data-dir", cfg.dataDir)
	if err == nil || !strings.Contains(err.Error(), "source and target data dirs are the same") {
		t.Fatalf("err = %v, want same-dir refusal", err)
	}
}
