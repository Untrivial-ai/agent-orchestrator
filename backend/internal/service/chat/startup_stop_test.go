package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStopChatStartupMissingRegistryDoesNotConfirmExit(t *testing.T) {
	service := New(Options{})
	confirmed, err := service.StopChatStartup(context.Background(), "worker", "before-restart")
	if err != nil || confirmed {
		t.Fatalf("missing registry confirmed=%v error=%v", confirmed, err)
	}
}

func TestStopChatStartupCannotStopReplacementGeneration(t *testing.T) {
	service := New(Options{})
	controller := &Controller{generation: "replacement"}
	service.controllers["worker"] = controller
	confirmed, err := service.StopChatStartup(context.Background(), "worker", "old-generation")
	if err != nil || confirmed {
		t.Fatalf("stale generation confirmed=%v error=%v", confirmed, err)
	}
	if service.controllers["worker"] != controller {
		t.Fatal("replacement controller ownership lost")
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
