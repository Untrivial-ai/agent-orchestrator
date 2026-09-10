package runtimeselect

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeBackend struct {
	createHandle ports.RuntimeHandle
	createErr    error
	calls        []string
	handles      []ports.RuntimeHandle
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

func (f *fakeBackend) IsSupervisedProcessAlive(_ context.Context, handle ports.RuntimeHandle, _ ports.SupervisedProcessRef) (bool, error) {
	f.record("supervised", handle)
	return true, nil
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
