// Package cloud implements AO's runtime port by delegating every session to a
// control-plane-managed sandbox. The coordinator daemon keeps the ordinary AO
// service/API surface, so desktop components do not branch for cloud projects.
package cloud

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const maxWorkspaceArchive = 24 << 20

// Options identifies the control plane and one coordinator's scoped capability.
type Options struct{ BaseURL, Token, WorkspaceID string }

// Runtime delegates AO runtime operations to isolated cloud sandboxes.
type Runtime struct {
	baseURL     string
	token       string
	workspaceID string
	client      *http.Client
}

// New validates options and creates a cloud runtime adapter.
func New(options Options) (*Runtime, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("valid cloud runtime API URL is required")
	}
	if strings.TrimSpace(options.Token) == "" || strings.TrimSpace(options.WorkspaceID) == "" {
		return nil, errors.New("cloud runtime token and workspace ID are required")
	}
	return &Runtime{baseURL: parsed.String(), token: strings.TrimSpace(options.Token), workspaceID: strings.TrimSpace(options.WorkspaceID), client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// FromEnvironment selects cloud execution when its API URL is configured.
func FromEnvironment() (*Runtime, bool, error) {
	baseURL := strings.TrimSpace(os.Getenv("AO_CLOUD_RUNTIME_API_URL"))
	if baseURL == "" {
		return nil, false, nil
	}
	runtime, err := New(Options{BaseURL: baseURL, Token: os.Getenv("AO_CLOUD_RUNTIME_TOKEN"), WorkspaceID: os.Getenv("AO_CLOUD_WORKSPACE_ID")})
	return runtime, true, err
}

type runtimeFile struct {
	Path       string `json:"path"`
	DataBase64 string `json:"dataBase64"`
}
type createRequest struct {
	SessionID               string            `json:"sessionId"`
	Branch                  string            `json:"branch,omitempty"`
	SourceWorkspace         string            `json:"sourceWorkspace"`
	Argv                    []string          `json:"argv"`
	Env                     map[string]string `json:"env,omitempty"`
	WorkspaceArchiveBase64  string            `json:"workspaceArchiveBase64,omitempty"`
	ClaudeCredentialsBase64 string            `json:"claudeCredentialsBase64"`
	Files                   []runtimeFile     `json:"files,omitempty"`
}
type runtimeRecord struct{ SessionID, SandboxID, State, Error string }
type statusResponse struct {
	Runtime runtimeRecord `json:"runtime"`
	Alive   bool          `json:"alive"`
	Output  string        `json:"output"`
}

// Create provisions a dedicated sandbox and waits until its agent is running.
func (r *Runtime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	archive, err := archiveWorkspace(ctx, cfg.WorkspacePath)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}
	credentials, err := os.ReadFile(filepath.Join(userHome(), ".claude", ".credentials.json"))
	if err != nil {
		return ports.RuntimeHandle{}, fmt.Errorf("read Claude credentials for isolated runtime: %w", err)
	}
	defer clear(credentials)
	files, err := referencedFiles(cfg.WorkspacePath, cfg.Argv)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}
	request := createRequest{
		SessionID: string(cfg.SessionID), Branch: cfg.Branch, SourceWorkspace: cfg.WorkspacePath,
		Argv: cfg.Argv, Env: cfg.Env, WorkspaceArchiveBase64: base64.StdEncoding.EncodeToString(archive),
		ClaudeCredentialsBase64: base64.StdEncoding.EncodeToString(credentials), Files: files,
	}
	clear(archive)
	var response statusResponse
	if err := r.requestJSON(ctx, http.MethodPost, r.runtimeURL(""), request, &response); err != nil {
		return ports.RuntimeHandle{}, err
	}
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	for {
		switch response.Runtime.State {
		case "running":
			return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
		case "failed":
			return ports.RuntimeHandle{}, fmt.Errorf("isolated runtime provisioning failed: %s", response.Runtime.Error)
		}
		select {
		case <-ctx.Done():
			return ports.RuntimeHandle{}, ctx.Err()
		case <-deadline.C:
			return ports.RuntimeHandle{}, errors.New("isolated runtime provisioning timed out")
		case <-poll.C:
			if err := r.requestJSON(ctx, http.MethodGet, r.runtimeURL(string(cfg.SessionID)), nil, &response); err != nil {
				return ports.RuntimeHandle{}, err
			}
		}
	}
}

// Destroy deletes the session's entire sandbox.
func (r *Runtime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	return r.requestJSON(ctx, http.MethodDelete, r.runtimeURL(handle.ID), nil, nil)
}

// GetOutput returns a bounded terminal capture from remote tmux.
func (r *Runtime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	var response statusResponse
	endpoint := r.runtimeURL(handle.ID)
	if lines > 0 {
		endpoint += "?lines=" + url.QueryEscape(fmt.Sprint(lines))
	}
	if err := r.requestJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	return response.Output, nil
}

// IsAlive reports whether the remote agent tmux session still exists.
func (r *Runtime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	var response statusResponse
	err := r.requestJSON(ctx, http.MethodGet, r.runtimeURL(handle.ID), nil, &response)
	return response.Alive, err
}

// Interrupt sends Ctrl-C to the remote agent.
func (r *Runtime) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	return r.requestJSON(ctx, http.MethodPost, r.runtimeURL(handle.ID)+"/interrupt", struct{}{}, nil)
}

// SendInput writes raw input without submitting it.
func (r *Runtime) SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error {
	return r.send(ctx, handle, input, false)
}

// SendMessage pastes and submits a message to the remote agent.
func (r *Runtime) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	return r.send(ctx, handle, message, true)
}
func (r *Runtime) send(ctx context.Context, handle ports.RuntimeHandle, input string, enter bool) error {
	return r.requestJSON(ctx, http.MethodPost, r.runtimeURL(handle.ID)+"/input", map[string]any{"input": input, "enter": enter}, nil)
}

// Attach opens a polling terminal stream over the HTTPS control-plane path.
func (r *Runtime) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	attachCtx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	result := &pollingStream{runtime: r, handle: handle, reader: reader, cancel: cancel}
	go result.poll(attachCtx, writer)
	return result, nil
}

type pollingStream struct {
	runtime *Runtime
	handle  ports.RuntimeHandle
	reader  *io.PipeReader
	cancel  context.CancelFunc
}

func (s *pollingStream) Read(value []byte) (int, error) { return s.reader.Read(value) }
func (s *pollingStream) Write(value []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if bytes.Equal(value, []byte{3}) {
		if err := s.runtime.Interrupt(ctx, s.handle); err != nil {
			return 0, err
		}
		return len(value), nil
	}
	input := string(value)
	enter := strings.HasSuffix(input, "\r") || strings.HasSuffix(input, "\n")
	input = strings.TrimSuffix(strings.TrimSuffix(input, "\n"), "\r")
	if err := s.runtime.send(ctx, s.handle, input, enter); err != nil {
		return 0, err
	}
	return len(value), nil
}
func (s *pollingStream) Close() error                { s.cancel(); return s.reader.Close() }
func (s *pollingStream) Resize(uint16, uint16) error { return nil }
func (s *pollingStream) poll(ctx context.Context, writer *io.PipeWriter) {
	defer func() { _ = writer.Close() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		output, err := s.runtime.GetOutput(ctx, s.handle, 500)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		var next string
		if strings.HasPrefix(output, last) {
			next = strings.TrimPrefix(output, last)
		} else if output != last {
			next = "\x1b[2J\x1b[H" + output
		}
		if next != "" {
			if _, err = io.WriteString(writer, next); err != nil {
				return
			}
		}
		last = output
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) runtimeURL(sessionID string) string {
	base := r.baseURL + "/api/cloud/internal/v1/workspaces/" + url.PathEscape(r.workspaceID) + "/runtimes"
	if sessionID != "" {
		base += "/" + url.PathEscape(sessionID)
	} else {
		base += "/"
	}
	return base
}

func (r *Runtime) requestJSON(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		value, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("cloud runtime API %s: %s", response.Status, strings.TrimSpace(string(value)))
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}

func archiveWorkspace(ctx context.Context, root string) ([]byte, error) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(gz)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		name := filepath.ToSlash(relative)
		first := strings.Split(name, "/")[0]
		if first == ".git" || first == "node_modules" || first == ".ao" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = name
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path) //nolint:gosec // filepath.Walk supplied this path beneath the coordinator-owned worktree.
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(archive, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if buffer.Len() > maxWorkspaceArchive {
			return errors.New("prepared workspace exceeds 24 MiB compressed cloud launch limit")
		}
		return nil
	})
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("archive cloud workspace: %w", err)
	}
	if buffer.Len() > maxWorkspaceArchive {
		return nil, errors.New("prepared workspace exceeds 24 MiB compressed cloud launch limit")
	}
	return buffer.Bytes(), nil
}

func referencedFiles(workspace string, argv []string) ([]runtimeFile, error) {
	files := make([]runtimeFile, 0)
	for index, argument := range argv {
		if index == 0 || !filepath.IsAbs(argument) || strings.HasPrefix(argument, workspace+string(os.PathSeparator)) {
			continue
		}
		info, err := os.Stat(argument)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > 1<<20 {
			return nil, fmt.Errorf("runtime file %s exceeds 1 MiB", argument)
		}
		data, err := os.ReadFile(argument)
		if err != nil {
			return nil, err
		}
		files = append(files, runtimeFile{Path: argument, DataBase64: base64.StdEncoding.EncodeToString(data)})
		clear(data)
	}
	return files, nil
}

func userHome() string {
	if value := strings.TrimSpace(os.Getenv("HOME")); value != "" {
		return value
	}
	value, _ := os.UserHomeDir()
	return value
}
