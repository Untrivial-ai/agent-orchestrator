package cua

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// RecordingResult is daemon-only metadata. Path is not accepted from workers.
// Start returns a process receipt; only Stop validates a finalized movie and
// fills Duration. Gap accompanies errors and must be journaled by the service.
type RecordingResult struct {
	Path           string        `json:"path"`
	MIMEType       string        `json:"mimeType"`
	Width          int           `json:"width"`
	Height         int           `json:"height"`
	Duration       time.Duration `json:"duration"`
	StartedAt      time.Time     `json:"startedAt"`
	StoppedAt      time.Time     `json:"stoppedAt"`
	RecorderPID    int           `json:"recorderPid"`
	Gap            string        `json:"gap,omitempty"`
	StagingPath    string        `json:"stagingPath,omitempty"`
	StagingCleanup string        `json:"stagingCleanup,omitempty"`
}

type recordingProcess struct {
	pid    int
	done   chan struct{}
	err    error // read only after done closes
	signal func(os.Signal) error
}

type windowRecording struct {
	result  RecordingResult
	process *recordingProcess
	birth   time.Time
	stopped bool
}

// StartRecording launches the built-in exact-window recorder to attempt storage.
// Start proves process ownership; Stop validates the finalized video itself.
func (a *Adapter) StartRecording(ctx context.Context, target domain.TestTargetIdentity, evidenceDir string) (RecordingResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := a.bound(ctx, target)
	if err != nil {
		return recordingGap(RecordingResult{}, err)
	}
	if b.recording != nil {
		return recordingGap(b.recording.result, refuse("recording_exists", "this binding already has a recording"))
	}
	if err := a.startSession(ctx, b); err != nil {
		return recordingGap(RecordingResult{}, err)
	}
	w, err := a.prepareWindow(ctx, b)
	if err != nil {
		return recordingGap(RecordingResult{}, err)
	}
	if !w.OnScreen || (w.OnCurrentSpace != nil && !*w.OnCurrentSpace) {
		return recordingGap(RecordingResult{}, refuse("recording_window_hidden", "window must be visible on the current Space for built-in video"))
	}
	dir, err := recordingDirectory(evidenceDir)
	if err != nil {
		return recordingGap(RecordingResult{}, err)
	}
	shot, err := a.screenshot(ctx, target)
	if err != nil {
		return recordingGap(RecordingResult{}, err)
	}
	w, err = a.liveWindow(ctx, b)
	if err != nil || !w.OnScreen || (w.OnCurrentSpace != nil && !*w.OnCurrentSpace) || w.Bounds != shot.Frame.Bounds {
		if err == nil {
			err = refuse("recording_window_changed", "window changed or became hidden before recording")
		}
		return recordingGap(RecordingResult{}, err)
	}
	stem := filepath.Join(dir, "window-"+uuid.NewString())
	original := shot.Frame
	if shot.Original != nil {
		original = shot.Original.Frame
	}
	result := RecordingResult{Path: filepath.Join(stem, "window.mp4"), MIMEType: "video/mp4", Width: original.Width,
		Height: original.Height, StartedAt: a.now().UTC(), StagingCleanup: "provider-managed staging; AO does not inspect external storage"}
	if err := os.Mkdir(stem, 0o700); err != nil {
		return recordingGap(result, err)
	}
	args := []string{"-v", "-o", "-x", "-l" + target.WindowID, filepath.Join(stem, "window.mov")}
	if err := ctx.Err(); err != nil {
		return recordingGap(result, err)
	}
	p, err := a.startRecorder(ctx, args, append(cleanEnvironment(), a.driverEnvironment()...), filepath.Join(stem, "stdout.log"), filepath.Join(stem, "stderr.log"))
	if err != nil {
		return recordingGap(result, err)
	}
	result.RecorderPID = p.pid
	r := &windowRecording{result: result, process: p}
	b.recording = r // retain ownership even if startup validation fails
	r.birth, err = a.started(context.WithoutCancel(ctx), p.pid)
	if err != nil {
		return recordingGap(result, fmt.Errorf("observe owned recorder identity: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return recordingGap(result, fmt.Errorf("owned recorder launch canceled for PID %d: %w", p.pid, err))
	}
	return result, nil
}

// StopRecording validates the stored launch identity but does not require the
// target or Driver to be alive. It signals only the owned recorder, waits for
// exit, then uses AVFoundation's built-in analyzer to validate the final file.
// A canceled caller can retry Stop; pending recording ownership is retained.
func (a *Adapter) StopRecording(ctx context.Context, target domain.TestTargetIdentity) (RecordingResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bindings[target.ID]
	if b == nil || !sameTarget(b.target, target) {
		return recordingGap(RecordingResult{}, refuse("target_not_bound", "recording target does not match stored binding"))
	}
	return a.stopRecording(ctx, b)
}

func (a *Adapter) stopRecording(ctx context.Context, b *binding) (result RecordingResult, err error) {
	r := b.recording
	if r == nil {
		return recordingGap(RecordingResult{}, refuse("recording_missing", "binding has no recording"))
	}
	// A forced stop remains a recording gap even if the resulting container
	// validates, and even when cleanup later retries finalization.
	defer func() {
		if r.result.Gap != "" {
			result, err = recordingGap(result, errors.Join(errors.New(r.result.Gap), err))
		}
	}()
	if r.stopped {
		return r.result, nil
	}
	p := r.process
	if _, err := a.signalRecorder(ctx, r, os.Interrupt); err != nil {
		return recordingGap(r.result, err)
	}
	if err := waitRecorder(ctx, p, a.interruptWait); err != nil {
		if ctx.Err() != nil {
			return recordingGap(r.result, fmt.Errorf("recorder finalization pending for PID %d: %w", p.pid, ctx.Err()))
		}
		sent, err := a.signalRecorder(ctx, r, syscall.SIGTERM)
		if err != nil {
			return recordingGap(r.result, fmt.Errorf("recorder PID %d did not exit after SIGINT: %w", p.pid, err))
		}
		if sent {
			r.result.Gap = fmt.Sprintf("recorder PID %d did not exit within %s after SIGINT; SIGTERM was required", p.pid, a.interruptWait)
		}
		if err := waitRecorder(ctx, p, a.terminateWait); err != nil {
			return recordingGap(r.result, fmt.Errorf("recorder finalization pending after SIGTERM for PID %d: %w", p.pid, err))
		}
	}
	r.result.StoppedAt = a.now().UTC()
	source := filepath.Join(filepath.Dir(r.result.Path), "window.mov")
	if _, err := a.validateMovie(ctx, source, r.result.Width, r.result.Height); err != nil {
		return recordingGap(r.result, errors.Join(err, p.err))
	}
	converted, err := a.run(ctx, "/usr/bin/avconvert", "--preset", "PresetPassthrough", "--source", source, "--output", r.result.Path, "--replace")
	if err != nil {
		detail := providerDiagnosticText(string(converted.Stderr)+string(converted.Stdout), nil, providerDiagnosticLimit)
		return recordingGap(r.result, &Error{Code: "recording_remux_failed", Detail: fmt.Sprintf("avconvert PresetPassthrough: %v: %s", err, detail), cause: errors.Join(ErrRefused, err, ctx.Err())})
	}
	duration, err := a.validateMovie(ctx, r.result.Path, r.result.Width, r.result.Height)
	if err != nil {
		return recordingGap(r.result, &Error{Code: "recording_remux_failed", Detail: fmt.Sprintf("validate remuxed MP4: %v", err), cause: errors.Join(ErrRefused, err)})
	}
	if err := os.Remove(source); err != nil {
		return recordingGap(r.result, &Error{Code: "recording_remux_failed", Detail: fmt.Sprintf("remove owned MOV source: %v", err), cause: errors.Join(ErrRefused, err)})
	}
	r.result.Duration, r.stopped = duration, true
	if p.err != nil {
		r.result.Gap = fmt.Sprintf("owned recorder failed before clean finalization: %v", p.err)
		return r.result, nil // deferred gap handling preserves the same result on retry
	}
	return r.result, nil
}

func (a *Adapter) validateMovie(ctx context.Context, path string, expectedWidth, expectedHeight int) (time.Duration, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		detail := fmt.Sprintf("recorder exited without a finalized movie: %v", err)
		if err == nil {
			detail = fmt.Sprintf("recorder output is not a positive regular movie: mode=%s size=%d", info.Mode(), info.Size())
		}
		return 0, errors.Join(refuse("recording_empty", detail), err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return 0, err
	}
	metadata, err := a.run(ctx, "/usr/bin/avmediainfo", path, "--chunks", "--mediatype", "video")
	if err != nil {
		return 0, &Error{Code: "recording_invalid", Detail: fmt.Sprintf("analyze finalized movie: %v: %s", err, strings.TrimSpace(string(metadata.Stderr))), cause: errors.Join(ErrRefused, err)}
	}
	duration, width, height, err := parseMovieInfo(string(metadata.Stdout))
	if err != nil || width != expectedWidth || height != expectedHeight {
		if err == nil {
			err = refuse("recording_invalid", fmt.Sprintf("video dimensions %dx%d do not match the captured window %dx%d", width, height, expectedWidth, expectedHeight))
		}
		return 0, err
	}
	return duration, nil
}

func (a *Adapter) signalRecorder(ctx context.Context, r *windowRecording, signal os.Signal) (bool, error) {
	p := r.process
	select {
	case <-p.done:
		return false, nil
	default:
	}
	birth, err := a.started(context.WithoutCancel(ctx), p.pid)
	if err != nil || r.birth.IsZero() || !birth.Equal(r.birth) || p.pid != r.result.RecorderPID {
		select {
		case <-p.done: // the owned recorder may have exited during the probe
			return false, nil
		default:
			return false, refuse("recorder_changed", "owned recorder PID and birth identity cannot be proved")
		}
	}
	if err := p.signal(signal); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return false, nil
		}
		return false, fmt.Errorf("signal %s to owned recorder PID %d: %w", signal, p.pid, err)
	}
	return true, nil
}

func waitRecorder(ctx context.Context, p *recordingProcess, timeout time.Duration) error {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-p.done:
		return nil
	case <-deadline.Done():
		return deadline.Err()
	}
}

func recordingGap(result RecordingResult, err error) (RecordingResult, error) {
	result.Gap = err.Error()
	return result, err
}

func recordingDirectory(dir string) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", refuse("recording_storage", "evidence directory must be an absolute clean daemon-owned path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", refuse("recording_storage", "evidence directory must be a real directory")
	}
	return filepath.EvalSymlinks(dir)
}

var movieDuration = regexp.MustCompile(`(?m)^Duration: (\d+(?:\.\d+)?) seconds`)
var movieDimensions = regexp.MustCompile(`Dimensions: (\d+) x (\d+)`)
var movieSamples = regexp.MustCompile(`(?m)^\s+\d+\s+[1-9]\d*\s+\[`)

func parseMovieInfo(text string) (time.Duration, int, int, error) {
	d, dimensions := movieDuration.FindStringSubmatch(text), movieDimensions.FindStringSubmatch(text)
	if len(d) != 2 || len(dimensions) != 3 || !strings.Contains(text, "Track count: 1\n") ||
		!strings.Contains(text, "System support for decoding this track: Yes") || !strings.Contains(text, "Movie analyzed with 0 error.") {
		return 0, 0, 0, refuse("recording_invalid", "AVFoundation did not validate one decodable video track")
	}
	if !movieSamples.MatchString(text) {
		return 0, 0, 0, refuse("recording_empty", "video track has no indexed frames")
	}
	seconds, err := time.ParseDuration(d[1] + "s")
	if err != nil || seconds <= 0 {
		return 0, 0, 0, refuse("recording_invalid", "video duration must be positive")
	}
	w, errW := strconv.Atoi(dimensions[1])
	h, errH := strconv.Atoi(dimensions[2])
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, 0, refuse("recording_invalid", "invalid video dimensions")
	}
	return seconds, w, h, nil
}

func startScreencapture(ctx context.Context, args, env []string, stdoutPath, stderrPath string) (*recordingProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.Join(err, stdout.Close())
	}
	cmd := processutil.Command("/usr/sbin/screencapture", args...)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Hold stdin open until SIGINT; EOF does not finalize a movie reliably.
	stdin, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		if stdin != nil {
			err = errors.Join(err, stdin.Close())
		}
		return nil, errors.Join(err, stdout.Close(), stderr.Close())
	}
	p := &recordingProcess{pid: cmd.Process.Pid, done: make(chan struct{}), signal: cmd.Process.Signal}
	go func() {
		p.err = errors.Join(cmd.Wait(), stdout.Close(), stderr.Close())
		if p.err != nil {
			data, readErr := os.ReadFile(stderrPath)
			if readErr == nil && len(data) != 0 {
				p.err = errors.Join(p.err, fmt.Errorf("screencapture: %s", strings.TrimSpace(string(data[:min(len(data), 4096)]))))
			}
		}
		close(p.done)
	}()
	return p, nil
}
