package cua

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const validMovieInfo = "Duration: 3.368 seconds (2021/600)\nTrack count: 1\nTrack 1: Video 'vide'\n\tDimensions: 1280 x 800\n\tSystem support for decoding this track: Yes\nChunks\n  1  1  [1]  0x24  64  00:00:00.000  SDF\nMovie analyzed with 0 error.\n"

type fakeRecording struct {
	process    *recordingProcess
	args       []string
	env        []string
	signals    []os.Signal
	path       string
	info       string
	finish     bool
	staged     string
	remuxErr   error
	remuxInfo  string
	remuxCalls int
	analyzed   []string
}

func prepareRecording(t *testing.T, f *fixture) *fakeRecording {
	t.Helper()
	r := &fakeRecording{process: &recordingProcess{pid: 42, done: make(chan struct{})}, info: validMovieInfo, finish: true}
	r.staged = filepath.Join(f.adapter.cfg.DataDir, "fake-encoder.mov")
	f.adapter.startRecorder = func(_ context.Context, args, env []string, _, _ string) (*recordingProcess, error) {
		r.args, r.env = args, env
		r.path = args[len(args)-1]
		if err := os.WriteFile(r.staged, []byte("native movie staging"), 0o600); err != nil {
			return nil, err
		}
		return r.process, nil
	}
	r.process.signal = func(signal os.Signal) error {
		r.signals = append(r.signals, signal)
		if r.finish {
			if err := os.Rename(r.staged, r.path); err != nil {
				return err
			}
			close(r.process.done)
		}
		return nil
	}
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		mp4 := filepath.Join(filepath.Dir(r.path), "window.mp4")
		if executable == "/usr/bin/avconvert" {
			want := []string{"--preset", "PresetPassthrough", "--source", r.path, "--output", mp4}
			replace := len(args) == len(want)+1 && args[len(want)] == "--replace"
			actual := args
			if replace {
				actual = args[:len(want)]
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("remux escaped the owned files or changed codec preset: %v", args)
			}
			r.remuxCalls++
			if _, err := os.Lstat(mp4); err == nil && !replace {
				return Output{Stderr: []byte("output file already exists; use --replace")}, os.ErrExist
			}
			if r.remuxErr != nil {
				return Output{Stderr: []byte("owned MP4 output denied")}, r.remuxErr
			}
			data, err := os.ReadFile(r.path)
			if err != nil {
				return Output{}, err
			}
			return Output{}, os.WriteFile(mp4, data, 0o600)
		}
		if executable == "/usr/bin/avmediainfo" {
			if len(args) != 4 || args[0] != r.path && args[0] != mp4 || !reflect.DeepEqual(args[1:], []string{"--chunks", "--mediatype", "video"}) {
				t.Fatalf("analyzed wrong movie: %v", args)
			}
			r.analyzed = append(r.analyzed, args[0])
			if args[0] == mp4 && r.remuxInfo != "" {
				return Output{Stdout: []byte(r.remuxInfo)}, nil
			}
			return Output{Stdout: []byte(r.info)}, nil
		}
		return provider(executable, args)
	}
	return r
}

func startFakeRecording(t *testing.T, f *fixture) RecordingResult {
	t.Helper()
	result, err := f.adapter.StartRecording(context.Background(), f.target, filepath.Join(f.adapter.cfg.DataDir, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRecordingWindowOnlyAndSIGINTFinalization(t *testing.T) {
	t.Setenv("AO_FORBIDDEN", "secret")
	cause := errors.New("passthrough failed")
	for _, tc := range []struct {
		name, remuxInfo, reason string
		remuxErr                error
	}{
		{name: "validated MP4"},
		{name: "remux failed", remuxErr: cause, reason: "owned MP4 output denied"},
		{name: "invalid MP4", remuxInfo: "invalid MP4", reason: "validate remuxed MP4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			r := prepareRecording(t, f)
			r.remuxErr, r.remuxInfo = tc.remuxErr, tc.remuxInfo
			start := startFakeRecording(t, f)
			if !reflect.DeepEqual(r.args, []string{"-v", "-o", "-x", "-l456", filepath.Join(filepath.Dir(start.Path), "window.mov")}) || start.Duration != 0 || start.Gap != "" || start.MIMEType != "video/mp4" {
				t.Fatalf("wrong window recorder receipt or arguments: %+v %v", start, r.args)
			}
			for _, entry := range r.env {
				if strings.HasPrefix(entry, "AO_") {
					t.Fatal("inherited AO setting reached recorder")
				}
			}
			root, err := filepath.EvalSymlinks(filepath.Join(f.adapter.cfg.DataDir, "evidence"))
			if err != nil || filepath.Dir(filepath.Dir(start.Path)) != root {
				t.Fatalf("movie escaped evidence dir: %s", start.Path)
			}
			final, err := f.adapter.StopRecording(context.Background(), f.target)
			if !reflect.DeepEqual(r.signals, []os.Signal{os.Interrupt}) || r.remuxCalls != 1 {
				t.Fatalf("wrong recorder shutdown or remux count: signals=%v remux=%d", r.signals, r.remuxCalls)
			}
			wantAnalyzed := []string{r.path}
			if tc.remuxErr == nil {
				wantAnalyzed = append(wantAnalyzed, start.Path)
			}
			if !reflect.DeepEqual(r.analyzed, wantAnalyzed) {
				t.Fatalf("MOV and MP4 validation order changed: %v", r.analyzed)
			}
			if tc.reason != "" {
				var remuxErr *Error
				if !errors.As(err, &remuxErr) || remuxErr.Code != "recording_remux_failed" || !strings.Contains(final.Gap, tc.reason) || final.Duration != 0 || tc.remuxErr != nil && !errors.Is(err, tc.remuxErr) {
					t.Fatalf("remux failure lost its exact reason: %+v %v", final, err)
				}
				return
			}
			if err != nil || final.Duration != 3368*time.Millisecond || final.Width != 1280 || final.Height != 800 || final.Gap != "" || final.Path != start.Path || final.MIMEType != "video/mp4" || filepath.Ext(final.Path) != ".mp4" {
				t.Fatalf("MP4 was not validated: %+v %v", final, err)
			}
			if _, err := os.Lstat(r.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned MOV remained after successful remux: %v", err)
			}
			if final.StagingPath != "" || final.StagingCleanup != "provider-managed staging; AO does not inspect external storage" {
				t.Fatalf("staging UUID not verified: %+v", final)
			}
			again, err := f.adapter.StopRecording(context.Background(), f.target)
			if err != nil || again != final || len(r.signals) != 1 || r.remuxCalls != 1 {
				t.Fatalf("stop was not idempotent: %+v %v", again, err)
			}
		})
	}
}

func TestRecordingKeepsOriginalDimensionsAfterPreviewResize(t *testing.T) {
	f := newFixture(t)
	f.width, f.height = 2640, 1560
	f.bounds.Width, f.bounds.Height = 1320, 780
	r := prepareRecording(t, f)
	r.info = strings.ReplaceAll(validMovieInfo, "1280 x 800", "2640 x 1560")
	start := startFakeRecording(t, f)
	if start.Width != 2640 || start.Height != 1560 {
		t.Fatalf("video inherited preview dimensions: %+v", start)
	}
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err != nil || result.Width != 2640 || result.Height != 1560 || result.Duration <= 0 {
		t.Fatalf("original-size movie failed validation: %+v %v", result, err)
	}
}

func TestRecordingStopAfterTargetClosed(t *testing.T) {
	f := newFixture(t)
	prepareRecording(t, f)
	startFakeRecording(t, f)
	f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
		if pid == f.target.ElectronPID || pid == f.adapter.driver.pid {
			return time.Time{}, errors.New("target and Driver already gone")
		}
		return f.born, nil
	}
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err != nil || result.Duration <= 0 || result.Gap != "" {
		t.Fatalf("closed target prevented movie finalization: %+v %v", result, err)
	}
}

func TestRecordingPreservesProviderFailureWithPlayableMovie(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	startFakeRecording(t, f)
	r.process.err = errors.New("recorder failed")
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err == nil || result.Duration <= 0 || !strings.Contains(result.Gap, "recorder failed") {
		t.Fatalf("recorder failure was hidden: %+v %v", result, err)
	}
	again, err := f.adapter.StopRecording(context.Background(), f.target)
	if err == nil || again.Gap != result.Gap || len(r.signals) != 1 {
		t.Fatalf("retry erased recorder failure: %+v %v", again, err)
	}
}

func TestRecordingEscalatesIgnoredSIGINTAfterTargetClosed(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	startFakeRecording(t, f)
	f.adapter.interruptWait = time.Millisecond
	var probed []int
	f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
		probed = append(probed, pid)
		if pid != r.process.pid {
			return time.Time{}, errors.New("target and Driver already gone")
		}
		return f.born, nil
	}
	r.process.signal = func(signal os.Signal) error {
		r.signals = append(r.signals, signal)
		if signal == syscall.SIGTERM {
			if err := os.Rename(r.staged, r.path); err != nil {
				return err
			}
			close(r.process.done)
		}
		return nil
	}
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if err == nil || !strings.Contains(result.Gap, "SIGINT; SIGTERM was required") || result.StoppedAt.IsZero() {
		t.Fatalf("forced stop lost its gap: %+v %v", result, err)
	}
	if !reflect.DeepEqual(r.signals, []os.Signal{os.Interrupt, syscall.SIGTERM}) || !reflect.DeepEqual(probed, []int{42, 42}) {
		t.Fatalf("signals lacked exact recorder probes: signals=%v probes=%v", r.signals, probed)
	}
	if result.StagingPath != "" || result.StagingCleanup != "provider-managed staging; AO does not inspect external storage" {
		t.Fatalf("forced stop changed staging audit: %+v", result)
	}
	again, err := f.adapter.StopRecording(context.Background(), f.target)
	if err == nil || again.Gap != result.Gap || len(r.signals) != 2 {
		t.Fatalf("retry erased forced-stop gap: %+v %v", again, err)
	}
}

func TestRecordingEscalationRechecksBirthAndPID(t *testing.T) {
	for _, changePID := range []bool{false, true} {
		t.Run(map[bool]string{false: "birth", true: "PID"}[changePID], func(t *testing.T) {
			f := newFixture(t)
			r := prepareRecording(t, f)
			r.finish = false
			startFakeRecording(t, f)
			f.adapter.interruptWait = time.Millisecond
			f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
				if len(r.signals) > 0 && !changePID {
					return f.born.Add(time.Microsecond), nil
				}
				return f.born, nil
			}
			interrupt := r.process.signal
			r.process.signal = func(signal os.Signal) error {
				err := interrupt(signal)
				if changePID {
					r.process.pid++
				}
				return err
			}
			result, err := f.adapter.StopRecording(context.Background(), f.target)
			if !errors.Is(err, ErrRefused) || !strings.Contains(result.Gap, "recorder_changed") || !reflect.DeepEqual(r.signals, []os.Signal{os.Interrupt}) {
				t.Fatalf("changed recorder received SIGTERM: %+v %v signals=%v", result, err, r.signals)
			}
		})
	}
}

func TestRecordingSIGTERMWaitIsBounded(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	r.finish = false
	startFakeRecording(t, f)
	f.adapter.interruptWait, f.adapter.terminateWait = time.Millisecond, time.Millisecond
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(result.Gap, "pending after SIGTERM") || !result.StoppedAt.IsZero() || !reflect.DeepEqual(r.signals, []os.Signal{os.Interrupt, syscall.SIGTERM}) {
		t.Fatalf("SIGTERM wait did not retain pending gap: %+v %v signals=%v", result, err, r.signals)
	}
	if _, err := os.Stat(r.staged); err != nil {
		t.Fatalf("unproved staging file was touched: %v", err)
	}
}

func TestRecordingRefusesHiddenTargetAndChangedRecorder(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	f.hidden = true
	result, err := f.adapter.StartRecording(context.Background(), f.target, filepath.Join(f.adapter.cfg.DataDir, "evidence"))
	if !errors.Is(err, ErrRefused) || result.Gap == "" || len(r.args) != 0 {
		t.Fatalf("hidden window reached recorder: %+v %v", result, err)
	}
	f.hidden = false
	startFakeRecording(t, f)
	f.adapter.started = func(_ context.Context, _ int) (time.Time, error) { return f.born.Add(time.Microsecond), nil }
	result, err = f.adapter.StopRecording(context.Background(), f.target)
	if !errors.Is(err, ErrRefused) || result.Gap == "" || len(r.signals) != 0 {
		t.Fatalf("changed recorder received a signal: %+v %v", result, err)
	}
}

func TestRecordingCancellationRetainsOwnership(t *testing.T) {
	for _, stage := range []string{"recorder finalization", "MP4 validation"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			r := prepareRecording(t, f)
			r.finish = stage != "recorder finalization"
			start := startFakeRecording(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "recorder finalization" {
				cancel()
			} else {
				provider := f.runner.hook
				f.runner.hook = func(executable string, args []string) (Output, error) {
					if executable == "/usr/bin/avmediainfo" && args[0] == start.Path && ctx.Err() == nil {
						cancel()
						return Output{}, ctx.Err()
					}
					return provider(executable, args)
				}
			}
			result, err := f.adapter.StopRecording(ctx, f.target)
			if !errors.Is(err, context.Canceled) || result.Gap == "" || len(r.signals) != 1 {
				t.Fatalf("cancellation lost recorder cleanup: %+v %v", result, err)
			}
			if stage == "recorder finalization" {
				if err := os.Rename(r.staged, r.path); err != nil {
					t.Fatal(err)
				}
				close(r.process.done)
			} else {
				for _, path := range []string{r.path, start.Path} {
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("cancelled validation did not leave owned MOV and MP4: %v", err)
					}
				}
			}
			result, err = f.adapter.StopRecording(context.Background(), f.target)
			if err != nil || result.Duration <= 0 || len(r.signals) != 1 {
				t.Fatalf("retry lost movie or signaled recorder twice: %+v %v", result, err)
			}
			if _, err := os.Stat(r.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retry left owned MOV: %v", err)
			}
		})
	}
}

func TestRecordingRejectsInvalidMovieAndForeignStop(t *testing.T) {
	f := newFixture(t)
	r := prepareRecording(t, f)
	startFakeRecording(t, f)
	foreign := f.target
	foreign.WindowID = "999"
	if result, err := f.adapter.StopRecording(context.Background(), foreign); !errors.Is(err, ErrRefused) || result.Gap == "" || len(r.signals) != 0 {
		t.Fatalf("foreign target stopped recorder: %+v %v", result, err)
	}
	r.info = strings.ReplaceAll(validMovieInfo, "1280 x 800", "2880 x 1800")
	result, err := f.adapter.StopRecording(context.Background(), f.target)
	if !errors.Is(err, ErrRefused) || result.Gap == "" || result.Duration != 0 {
		t.Fatalf("display dimensions accepted as window video: %+v %v", result, err)
	}
	for _, info := range []string{"", strings.ReplaceAll(validMovieInfo, "  1  1  [1]", "  1  0  [0]"), strings.ReplaceAll(validMovieInfo, "3.368", "0"), strings.ReplaceAll(validMovieInfo, "Track count: 1", "Track count: 2"), strings.ReplaceAll(validMovieInfo, "0 error", "1 error"), strings.ReplaceAll(validMovieInfo, "track: Yes", "track: No")} {
		if _, _, _, err := parseMovieInfo(info); err == nil {
			t.Fatalf("invalid movie metadata accepted: %q", info)
		}
	}
}

func TestCloseStopsRecorderAndDriverEvenForInvalidMovie(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "movie gap"}[invalid], func(t *testing.T) {
			f := newFixture(t)
			r := prepareRecording(t, f)
			startFakeRecording(t, f)
			if invalid {
				r.info = "invalid movie"
			}
			stopped := false
			provider := f.runner.hook
			f.runner.hook = func(executable string, args []string) (Output, error) {
				if len(args) == 5 && args[3] == "end_session" {
					return jsonOutput(map[string]any{}), nil
				}
				if len(args) == 7 && args[4] == "stop" {
					if args[5] != "--expected-pid" || args[6] != "99" {
						t.Fatal("Driver stop lost owned PID")
					}
					stopped = true
					return Output{}, os.Remove(f.adapter.pidFile())
				}
				return provider(executable, args)
			}
			f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
				if pid == 99 && stopped {
					return time.Time{}, process.ErrNotRunning
				}
				return f.born, nil
			}
			err := f.adapter.Close(context.Background())
			if (err != nil) != invalid || !stopped || f.adapter.driver.pid != 0 || len(r.signals) != 1 {
				t.Fatalf("Close left recorder/Driver behind: invalid=%t err=%v driver=%+v signals=%v", invalid, err, f.adapter.driver, r.signals)
			}
		})
	}
}

func TestRecordingStartCancellationRetainsOwnership(t *testing.T) {
	f := newFixture(t)
	prepareRecording(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	start := f.adapter.startRecorder
	f.adapter.startRecorder = func(ctx context.Context, args, env []string, stdout, stderr string) (*recordingProcess, error) {
		p, err := start(ctx, args, env, stdout, stderr)
		cancel()
		return p, err
	}
	result, err := f.adapter.StartRecording(ctx, f.target, filepath.Join(f.adapter.cfg.DataDir, "evidence"))
	if !errors.Is(err, context.Canceled) || result.RecorderPID != 42 {
		t.Fatalf("canceled launch lost its process receipt: %+v %v", result, err)
	}
	if f.adapter.bindings[f.target.ID].recording == nil {
		t.Fatal("startup cancellation lost cleanup ownership")
	}
	if _, err := f.adapter.StopRecording(context.Background(), f.target); err != nil {
		t.Fatal(err)
	}
}
