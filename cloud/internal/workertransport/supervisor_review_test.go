package workertransport

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestTerminalCommandUsesFreshReviewCommand(t *testing.T) {
	supervisor := Supervisor{
		AgentCommand:  workerexec.Command{Path: "codex", Args: []string{"resume", "active-thread"}},
		ReviewCommand: workerexec.Command{Path: "codex", Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
	}

	command, _, err := supervisor.terminalCommand(context.Background(), worker.TerminalCommand{
		TerminalID: "review-terminal",
		Kind:       "agent",
		Review:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Args) != 2 || command.Args[0] != "codex" || command.Args[1] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("review command args = %#v, want fresh review command", command.Args)
	}
}

func TestWriteReviewPromptSubmitsEnterSeparately(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	supervisor := &Supervisor{
		terminals: map[string]*terminalProcess{
			"review-terminal": {pty: writer, cancel: func() {}, cleanup: func() {}},
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- supervisor.writeReviewPrompt("review-terminal", []byte("review this change\r"))
	}()

	body := make([]byte, len("review this change"))
	if err := reader.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set body read deadline: %v", err)
	}
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("read review prompt body: %v", err)
	}
	if got := string(body); got != "review this change" {
		t.Fatalf("review prompt body = %q, want prompt without enter", got)
	}

	enter := make([]byte, 1)
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set enter read deadline: %v", err)
	}
	if _, err := io.ReadFull(reader, enter); err != nil {
		t.Fatalf("read review prompt enter: %v", err)
	}
	if got := string(enter); got != "\r" {
		t.Fatalf("review prompt enter = %q, want carriage return", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("write review prompt: %v", err)
	}
}
