package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStopChatStartupMissingRegistryDoesNotConfirmExit(t *testing.T) {
	service := New(Options{StopProviderHost: func(context.Context, domain.SessionID) error {
		t.Fatal("missing controller must not request host shutdown")
		return nil
	}})
	confirmed, err := service.StopChatStartup(context.Background(), "worker", "before-restart")
	if err != nil || confirmed {
		t.Fatalf("missing registry confirmed=%v error=%v", confirmed, err)
	}
}

func TestStopChatStartupCannotStopReplacementGeneration(t *testing.T) {
	service := New(Options{StopProviderHost: func(context.Context, domain.SessionID) error {
		t.Fatal("stale generation must not request host shutdown")
		return nil
	}})
	controller := &Controller{generation: "replacement", preserveProviderOnStop: true}
	service.controllers["worker"] = controller
	confirmed, err := service.StopChatStartup(context.Background(), "worker", "old-generation")
	if err != nil || confirmed {
		t.Fatalf("stale generation confirmed=%v error=%v", confirmed, err)
	}
	if service.controllers["worker"] != controller {
		t.Fatal("replacement controller ownership lost")
	}
}

func TestStopChatStartupRetainsDetachedProviderAfterShutdownRequest(t *testing.T) {
	callbackErr := errors.New("host shutdown request failed")
	for _, tc := range []struct {
		name        string
		callback    bool
		callbackErr error
	}{
		{name: "missing callback"},
		{name: "acknowledged shutdown", callback: true},
		{name: "failed shutdown", callback: true, callbackErr: callbackErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := New(Options{})
			probe := &startupTerminationProbe{stopped: make(chan struct{})}
			close(probe.stopped)
			controller := &Controller{
				generation: "owned", conv: probe, stopped: probe.stopped,
				preserveProviderOnStop: true,
			}
			// Projection failure closes the connection and consumes this hook
			// while the persistent provider keeps running.
			controller.once.Do(func() {})
			service.controllers["worker"] = controller
			service.startConfigs["worker"] = StartConfig{}
			calls := 0
			if tc.callback {
				service.stopProviderHost = func(_ context.Context, id domain.SessionID) error {
					calls++
					if id != "worker" || service.controllers[id] != controller {
						t.Fatal("shutdown lost exact controller ownership")
					}
					gate := service.controllerGate(id)
					select {
					case gate <- struct{}{}:
						gate.unlock()
						t.Fatal("shutdown callback did not hold session gate")
					default:
					}
					return tc.callbackErr
				}
			}
			confirmed, err := service.StopChatStartup(context.Background(), "worker", "owned")
			if confirmed || err == nil || (tc.callbackErr != nil && !errors.Is(err, tc.callbackErr)) {
				t.Fatalf("shutdown request confirmed=%v error=%v", confirmed, err)
			}
			if service.controllers["worker"] != controller {
				t.Fatal("unconfirmed provider controller ownership lost")
			}
			if _, exists := service.startConfigs["worker"]; !exists {
				t.Fatal("unconfirmed provider start configuration lost")
			}
			wantCalls := 0
			if tc.callback {
				wantCalls = 1
			}
			if calls != wantCalls || probe.calls != 0 {
				t.Fatalf("host shutdown calls=%d, consumed termination hook calls=%d", calls, probe.calls)
			}
		})
	}
}

type startupPersistentTerminationProbe struct {
	*startupTerminationProbe
}

func (*startupPersistentTerminationProbe) PreservesProviderOnClose() bool { return true }

func TestStopChatStartupRetainsPersistentProviderAfterTerminationAcknowledgement(t *testing.T) {
	service := New(Options{})
	probe := &startupPersistentTerminationProbe{
		startupTerminationProbe: &startupTerminationProbe{stopped: make(chan struct{})},
	}
	controller := &Controller{generation: "owned", conv: probe, stopped: probe.stopped}
	service.controllers["worker"] = controller
	confirmed, err := service.StopChatStartup(context.Background(), "worker", "owned")
	if confirmed || err == nil || probe.calls != 1 {
		t.Fatalf("termination acknowledgement confirmed=%v error=%v calls=%d", confirmed, err, probe.calls)
	}
	if service.controllers["worker"] != controller {
		t.Fatal("unconfirmed provider controller ownership lost")
	}
}

type startupTerminationProbe struct {
	ports.ChatConversation
	stopped chan struct{}
	err     error
	calls   int
}

func (p *startupTerminationProbe) Terminate() error {
	p.calls++
	if p.err == nil {
		close(p.stopped)
	}
	return p.err
}

func TestStopChatStartupWaitsForOwnedProviderAndRetainsFailedHandle(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "confirmed"
		if fails {
			name = "unconfirmed"
		}
		t.Run(name, func(t *testing.T) {
			service := New(Options{})
			probe := &startupTerminationProbe{stopped: make(chan struct{})}
			if fails {
				probe.err = errors.New("provider shutdown failed")
			}
			controller := &Controller{generation: "owned", conv: probe, stopped: probe.stopped}
			service.controllers["worker"] = controller
			// A stopped stream is not sufficient when provider termination failed.
			if fails {
				close(probe.stopped)
			}
			confirmed, err := service.StopChatStartup(context.Background(), "worker", "owned")
			if confirmed == fails || !errors.Is(err, probe.err) {
				t.Fatalf("confirmed=%v error=%v", confirmed, err)
			}
			_, exists := service.controllers["worker"]
			if exists != fails || probe.calls != 1 {
				t.Fatalf("handle retained=%v termination calls=%d", exists, probe.calls)
			}
		})
	}
}
