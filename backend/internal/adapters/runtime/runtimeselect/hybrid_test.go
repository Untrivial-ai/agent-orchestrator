package runtimeselect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/testutil/reviewerupdate"
)

type fakeBackend struct {
	createHandle  ports.RuntimeHandle
	createErr     error
	calls         []string
	handles       []ports.RuntimeHandle
	childAlive    bool
	childErr      error
	workloadRef   ports.SupervisedProcessRef
	workloadErr   error
	workloadAlive *bool
}

func (f *fakeBackend) record(call string, handle ports.RuntimeHandle) {
	f.calls = append(f.calls, call)
	f.handles = append(f.handles, handle)
}

func (f *fakeBackend) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	f.calls = append(f.calls, "create:"+string(cfg.SessionID))
	if f.createErr != nil {
		return ports.RuntimeHandle{}, f.createErr
	}
	if f.createHandle.ID != "" {
		return f.createHandle, nil
	}
	return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
}

func (f *fakeBackend) Destroy(_ context.Context, handle ports.RuntimeHandle) error {
	f.record("destroy", handle)
	return nil
}

func (f *fakeBackend) GetOutput(_ context.Context, handle ports.RuntimeHandle, _ int) (string, error) {
	f.record("output", handle)
	return "output", nil
}

func (f *fakeBackend) GetStyledOutput(_ context.Context, handle ports.RuntimeHandle, _ int) (string, error) {
	f.record("styled", handle)
	return "styled", nil
}

func (f *fakeBackend) IsAlive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	f.record("alive", handle)
	return true, nil
}

func (f *fakeBackend) IsChildAlive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	f.record("child", handle)
	return f.childAlive, f.childErr
}

func TestHybridRuntimeRoutesChildLiveness(t *testing.T) {
	for _, prefix := range []string{"", directHandlePrefix} {
		for _, probeErr := range []error{nil, ports.ErrRuntimeProbeInconclusive} {
			legacy, direct := &restartableFakeBackend{}, &fakeBackend{}
			backend := &legacy.fakeBackend
			if prefix != "" {
				backend = direct
			}
			backend.childErr = probeErr
			runtime := newHybridRuntime(legacy, direct, nil, "Linux")
			alive, err := runtime.IsChildAlive(context.Background(), ports.RuntimeHandle{ID: prefix + "shell"})
			if alive || !errors.Is(err, probeErr) {
				t.Fatalf("child status for %q = %v, %v; want false, %v", prefix, alive, err, probeErr)
			}
			if !reflect.DeepEqual(backend.calls, []string{"child"}) || backend.handles[0].ID != "shell" {
				t.Fatalf("child route for %q = %v, %v", prefix, backend.calls, backend.handles)
			}
		}
	}
}

func (f *fakeBackend) ProbeFencedRuntime(_ context.Context, _ ports.FencedRuntimeRef) ports.FencedProbeResult {
	return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
}

func (f *fakeBackend) Attach(_ context.Context, handle ports.RuntimeHandle, _, _ uint16) (ports.Stream, error) {
	f.record("attach", handle)
	return fakeStream{}, nil
}

func (f *fakeBackend) Interrupt(_ context.Context, handle ports.RuntimeHandle) error {
	f.record("interrupt", handle)
	return nil
}

func (f *fakeBackend) SendInput(_ context.Context, handle ports.RuntimeHandle, _ string) error {
	f.record("input", handle)
	return nil
}

func (f *fakeBackend) SendMessage(_ context.Context, handle ports.RuntimeHandle, _ string) error {
	f.record("message", handle)
	return nil
}

func (f *fakeBackend) IsSupervisedProcessAlive(_ context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	f.record("supervised", handle)
	f.workloadRef = ref
	alive := f.workloadErr == nil
	if f.workloadAlive != nil {
		alive = *f.workloadAlive
	}
	return alive, f.workloadErr
}

func TestHybridRuntimePreservesWorkloadModeAndErrors(t *testing.T) {
	for _, prefix := range []string{"", directHandlePrefix} {
		for _, ref := range []ports.SupervisedProcessRef{{}, {SessionID: "worker", LaunchID: "generation"}} {
			for _, probeErr := range []error{nil, ports.ErrRuntimeProbeInconclusive} {
				legacy, direct := &restartableFakeBackend{}, &fakeBackend{}
				backend := &legacy.fakeBackend
				if prefix != "" {
					backend = direct
				}
				backend.workloadErr = probeErr
				rt := newHybridRuntime(legacy, direct, nil, "Linux")
				alive, err := rt.IsSupervisedProcessAlive(context.Background(), ports.RuntimeHandle{ID: prefix + "worker"}, ref)
				if alive != (probeErr == nil) || !errors.Is(err, probeErr) || backend.workloadRef != ref {
					t.Fatalf("workload route %q, ref %+v = %v, %v, ref %+v", prefix, ref, alive, err, backend.workloadRef)
				}
				if !reflect.DeepEqual(backend.calls, []string{"supervised"}) || backend.handles[0].ID != "worker" {
					t.Fatalf("workload route %q = %v, %v", prefix, backend.calls, backend.handles)
				}
			}
		}
	}
}

func (f *fakeBackend) IsExactSupervisedProcessAlive(_ context.Context, handle ports.RuntimeHandle, _ ports.SupervisedProcessRef) (bool, error) {
	f.record("exact", handle)
	return true, nil
}

type restartableFakeBackend struct{ fakeBackend }

func (f *restartableFakeBackend) Restart(_ context.Context, handle ports.RuntimeHandle, _ ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	f.record("restart", handle)
	return handle, nil
}

type fakeStream struct{}

func (fakeStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (fakeStream) Write(p []byte) (int, error) { return len(p), nil }
func (fakeStream) Close() error                { return nil }
func (fakeStream) Resize(_, _ uint16) error    { return nil }

func TestHybridRuntimeCreatesDirectHandle(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{}
	runtime := newHybridRuntime(legacy, direct, nil, "macOS")

	handle, err := runtime.Create(context.Background(), ports.RuntimeConfig{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != directHandlePrefix+"session-1" {
		t.Fatalf("handle = %q, want versioned direct handle", handle.ID)
	}
	if len(legacy.calls) != 0 {
		t.Fatalf("legacy calls = %v, want none", legacy.calls)
	}
}

func TestHybridRuntimeFallsBackToTmuxWhenDirectCreateFails(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{createErr: errors.New("host unavailable")}
	runtime := newHybridRuntime(legacy, direct, nil, "Linux")

	handle, err := runtime.Create(context.Background(), ports.RuntimeConfig{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "session-1" {
		t.Fatalf("fallback handle = %q, want unprefixed tmux handle", handle.ID)
	}
	if !reflect.DeepEqual(legacy.calls, []string{"create:session-1"}) {
		t.Fatalf("legacy calls = %v", legacy.calls)
	}
}

func TestHybridRuntimeReportsBothCreationFailures(t *testing.T) {
	directErr := errors.New("host unavailable")
	fallbackErr := errors.New("tmux unavailable")
	legacy := &restartableFakeBackend{fakeBackend: fakeBackend{createErr: fallbackErr}}
	direct := &fakeBackend{createErr: directErr}
	runtime := newHybridRuntime(legacy, direct, nil, "macOS")

	_, err := runtime.Create(context.Background(), ports.RuntimeConfig{SessionID: "session-1"})
	if !errors.Is(err, directErr) || !errors.Is(err, fallbackErr) {
		t.Fatalf("Create error = %v, want both direct and fallback failures", err)
	}
}

func TestHybridRuntimeRoutesPersistedLegacyHandlesToTmux(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{}
	runtime := newHybridRuntime(legacy, direct, nil, "Linux")
	ctx := context.Background()
	handle := ports.RuntimeHandle{ID: "existing-session"}
	ref := ports.SupervisedProcessRef{SessionID: domain.SessionID("existing-session"), LaunchID: "launch-1"}

	_ = runtime.Destroy(ctx, handle)
	_, _ = runtime.IsAlive(ctx, handle)
	stream, _ := runtime.Attach(ctx, handle, 24, 80)
	_ = stream.Close()
	_ = runtime.Interrupt(ctx, handle)
	_ = runtime.SendInput(ctx, handle, "x")
	_ = runtime.SendMessage(ctx, handle, "hello")
	_, _ = runtime.GetOutput(ctx, handle, 10)
	_, _ = runtime.GetStyledOutput(ctx, handle, 10)
	_, _ = runtime.IsSupervisedProcessAlive(ctx, handle, ref)
	_, _ = runtime.IsExactSupervisedProcessAlive(ctx, handle, ref)

	wantCalls := []string{"destroy", "alive", "attach", "interrupt", "input", "message", "output", "styled", "supervised", "exact"}
	if !reflect.DeepEqual(legacy.calls, wantCalls) {
		t.Fatalf("legacy calls = %v, want %v", legacy.calls, wantCalls)
	}
	for _, routed := range legacy.handles {
		if routed != handle {
			t.Fatalf("legacy received handle %q, want %q", routed.ID, handle.ID)
		}
	}
	if len(direct.calls) != 0 {
		t.Fatalf("direct calls = %v, want none", direct.calls)
	}
}

func TestHybridRuntimeRoutesVersionedHandlesToDirectHost(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{}
	runtime := newHybridRuntime(legacy, direct, nil, "macOS")
	handle := ports.RuntimeHandle{ID: directHandlePrefix + "new-session"}

	if _, err := runtime.GetOutput(context.Background(), handle, 10); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct.calls, []string{"output"}) {
		t.Fatalf("direct calls = %v", direct.calls)
	}
	if got := direct.handles[0].ID; got != "new-session" {
		t.Fatalf("direct handle = %q, want stripped session id", got)
	}
	if len(legacy.calls) != 0 {
		t.Fatalf("legacy calls = %v, want none", legacy.calls)
	}
}

func TestHybridRuntimeRestartPreservesBackend(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{}
	runtime := newHybridRuntime(legacy, direct, nil, "Linux")
	cfg := ports.RuntimeConfig{SessionID: "session-1"}

	legacyHandle, err := runtime.Restart(context.Background(), ports.RuntimeHandle{ID: "session-1"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if legacyHandle.ID != "session-1" {
		t.Fatalf("legacy restart handle = %q", legacyHandle.ID)
	}
	if !reflect.DeepEqual(legacy.calls, []string{"restart"}) || legacy.handles[0].ID != "session-1" {
		t.Fatalf("legacy restart calls = %v, handles = %v", legacy.calls, legacy.handles)
	}

	directHandle, err := runtime.Restart(context.Background(), ports.RuntimeHandle{ID: directHandlePrefix + "session-1"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if directHandle.ID != directHandlePrefix+"session-1" {
		t.Fatalf("direct restart handle = %q", directHandle.ID)
	}
	if !reflect.DeepEqual(direct.calls, []string{"destroy", "create:session-1"}) {
		t.Fatalf("direct restart calls = %v", direct.calls)
	}
	if direct.handles[0].ID != "session-1" {
		t.Fatalf("direct destroy handle = %q", direct.handles[0].ID)
	}
}

func TestHybridRuntimeRestartCanFallBackToTmux(t *testing.T) {
	legacy := &restartableFakeBackend{}
	direct := &fakeBackend{createErr: errors.New("replacement host unavailable")}
	runtime := newHybridRuntime(legacy, direct, nil, "macOS")
	cfg := ports.RuntimeConfig{SessionID: "session-1"}

	handle, err := runtime.Restart(context.Background(), ports.RuntimeHandle{
		ID: directHandlePrefix + "session-1",
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "session-1" {
		t.Fatalf("fallback handle = %q, want unprefixed tmux handle", handle.ID)
	}
	if !reflect.DeepEqual(direct.calls, []string{"destroy", "create:session-1"}) {
		t.Fatalf("direct calls = %v", direct.calls)
	}
	if !reflect.DeepEqual(legacy.calls, []string{"create:session-1"}) {
		t.Fatalf("legacy calls = %v", legacy.calls)
	}
}

func TestReviewerWorkloadUpdateRoutesSnapshot(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "restore"} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%t", mode, fallback), func(t *testing.T) {
				alive := true
				direct := &fakeBackend{workloadAlive: &alive, childAlive: true}
				legacy := &restartableFakeBackend{}
				selected, other := direct, &legacy.fakeBackend
				prefix := directHandlePrefix
				if fallback {
					direct.createErr = errors.New("fixture native host unavailable")
					legacy.workloadAlive = &alive
					legacy.childAlive = true
					selected, other = &legacy.fakeBackend, direct
					prefix = ""
				}
				rt := newHybridRuntime(legacy, direct, nil, "Linux")
				f := reviewerupdate.New(context.Background(), t, rt, mode, "/fixture/codex")
				f.Blocked(context.Background(), t, nil)
				selected.workloadErr = ports.ErrRuntimeProbeInconclusive
				f.Blocked(context.Background(), t, ports.ErrRuntimeProbeInconclusive)
				selected.workloadErr = nil
				alive = false
				f.Ready(context.Background(), t)
				if f.Result.HandleID != prefix+"review-worker" || selected.workloadRef != (ports.SupervisedProcessRef{}) {
					t.Fatalf("router lost unsupervised identity: %+v %+v", f.Result, selected.workloadRef)
				}
				for _, call := range other.calls {
					if call == "supervised" || call == "child" {
						t.Fatal("snapshot probed wrong backend")
					}
				}
			})
		}
	}
}
