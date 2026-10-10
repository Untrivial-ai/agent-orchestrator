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
	result        RecordingResult
	process       *recordingProcess
	birth         time.Time
	stopped       bool
	stagingBefore map[string]os.FileInfo
	stagingSeen   map[string]os.FileInfo
}

// StartRecording launches Apple's exact-window recorder with no audio, region
// or desktop fallback. The responsible supervisor app needs its own existing
// Screen Recording grant. Off-current-Space windows record black on macOS 26,
// so this method refuses a hidden window instead of activating it.
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
	w, err := a.liveWindow(ctx, b)
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
	result := RecordingResult{Path: stem + ".mov", MIMEType: "video/quicktime", Width: original.Width,
		Height: original.Height, StartedAt: a.now().UTC(), StagingCleanup: "pending: recording in progress"}
	f, err := os.OpenFile(result.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return recordingGap(result, err)
	}
	if err := f.Close(); err != nil {
		return recordingGap(result, err)
	}
	// screencapture moves a finalized staging file into place and refuses an
	// existing destination. Remove only our exclusive UUID reservation.
	if err := os.Remove(result.Path); err != nil {
		return recordingGap(result, err)
	}
	// -l names only the fenced window. -o removes its shadow so video matches
	// the original screenshot dimensions. Never use Cua's display recorder.
	args := []string{"-v", "-o", "-x", "-l" + target.WindowID, result.Path}
	if err := ctx.Err(); err != nil {
		return recordingGap(result, err)
	}
	stagingBefore, err := stagingSnapshot(a.stagingDir)
	if err != nil {
		return recordingGap(result, fmt.Errorf("snapshot native staging metadata: %w", err))
	}
	p, err := a.startRecorder(args, append(cleanEnvironment(), a.driverEnvironment()...), stem+".stdout.log", stem+".stderr.log")
	if err != nil {
		return recordingGap(result, err)
	}
	result.RecorderPID = p.pid
	r := &windowRecording{result: result, process: p, stagingBefore: stagingBefore, stagingSeen: make(map[string]os.FileInfo)}
	b.recording = r // retain ownership even if startup validation fails
	r.birth, err = a.started(context.WithoutCancel(ctx), p.pid)
	if err != nil {
		return recordingGap(result, fmt.Errorf("observe owned recorder identity: %w", err))
	}
	select {
	case <-p.done:
		return recordingGap(result, errors.Join(refuse("recording_start_failed", "recorder exited during startup"), p.err))
	default:
		return result, nil
	}
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
		if r.result.StagingPath != "" {
			if err := verifyStagingAbsent(r.result.StagingPath); err != nil {
				return recordingGap(r.result, err)
			}
		}
		return r.result, nil
	}
	p := r.process
	r.result.StagingCleanup = "pending: recorder has not finalized; staging ownership unproved"
	if observed, err := stagingSnapshot(a.stagingDir); err == nil {
		for path, info := range observed {
			if _, existed := r.stagingBefore[path]; !existed {
				r.stagingSeen[path] = info
			}
		}
	} else {
		r.result.StagingCleanup = "unresolved: staging metadata observation failed: " + err.Error()
	}
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
	if err := a.finishStaging(ctx, r); err != nil {
		return recordingGap(r.result, err)
	}
	info, err := os.Lstat(r.result.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return recordingGap(r.result, errors.Join(refuse("recording_empty", "recorder exited without a finalized movie"), p.err))
	}
	if err := os.Chmod(r.result.Path, 0o600); err != nil {
		return recordingGap(r.result, err)
	}
	metadata, err := a.run(ctx, "/usr/bin/avmediainfo", r.result.Path)
	if err != nil {
		return recordingGap(r.result, fmt.Errorf("analyze finalized movie: %w", err))
	}
	duration, width, height, err := parseMovieInfo(string(metadata.Stdout))
	if err != nil || width != r.result.Width || height != r.result.Height {
		if err == nil {
			err = refuse("recording_dimensions", "video dimensions no longer match the captured window")
		}
		return recordingGap(r.result, err)
	}
	r.result.Duration, r.stopped = duration, true
	// Window closure can end the stream with a nonzero status. A validated,
	// finalized movie is still usable evidence; validation above is mandatory.
	return r.result, nil
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

func parseMovieInfo(text string) (time.Duration, int, int, error) {
	d, dimensions := movieDuration.FindStringSubmatch(text), movieDimensions.FindStringSubmatch(text)
	if len(d) != 2 || len(dimensions) != 3 || !strings.Contains(text, "Track count: 1\n") ||
		!strings.Contains(text, "System support for decoding this track: Yes") || !strings.Contains(text, "Movie analyzed with 0 error.") {
		return 0, 0, 0, refuse("recording_invalid", "AVFoundation did not validate one decodable video track")
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

func startScreencapture(args, env []string, stdoutPath, stderrPath string) (*recordingProcess, error) {
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.Join(err, stdout.Close())
	}
	cmd := processutil.Command("/usr/sbin/screencapture", args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, stdout, stderr
	// Closed stdin ends open-ended screencapture during startup. Hold this
	// pipe open until SIGINT finalizes the movie; never write keystrokes.
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
		// Wait closes the stdin pipe itself after the child has exited.
		p.err = errors.Join(cmd.Wait(), stdout.Close(), stderr.Close())
		close(p.done)
	}()
	return p, nil
}
