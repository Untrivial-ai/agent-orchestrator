package workerexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/google/uuid"
)

type codexFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type codexRPC struct {
	input          io.Writer
	writeMu        sync.Mutex
	mu             sync.Mutex
	nextID         int64
	pending        map[int64]chan codexFrame
	done           chan struct{}
	onNotification func(codexFrame)
	onRequest      func(codexFrame)
}

func newCodexRPC(input io.Writer, output io.Reader) *codexRPC {
	c := &codexRPC{input: input, pending: make(map[int64]chan codexFrame), done: make(chan struct{})}
	go c.read(output)
	return c
}

func (c *codexRPC) read(output io.Reader) {
	defer close(c.done)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var frame codexFrame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		if len(frame.ID) > 0 && frame.Method == "" {
			var id int64
			if json.Unmarshal(frame.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			pending := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if pending != nil {
				pending <- frame
			}
			continue
		}
		if len(frame.ID) > 0 && frame.Method != "" {
			c.mu.Lock()
			callback := c.onRequest
			c.mu.Unlock()
			if callback != nil {
				go callback(frame)
			}
			continue
		}
		if frame.Method != "" {
			c.mu.Lock()
			callback := c.onNotification
			c.mu.Unlock()
			if callback != nil {
				callback(frame)
			}
		}
	}
}

func (c *codexRPC) send(frame any) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.input.Write(append(encoded, '\n'))
	return err
}

func (c *codexRPC) request(ctx context.Context, method string, params any, response any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	answer := make(chan codexFrame, 1)
	c.pending[id] = answer
	c.mu.Unlock()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	select {
	case frame := <-answer:
		if frame.Error != nil {
			return fmt.Errorf("Codex %s: %s (%d)", method, frame.Error.Message, frame.Error.Code)
		}
		if response != nil {
			return json.Unmarshal(frame.Result, response)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("Codex app-server disconnected")
	}
}

func (c *codexRPC) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

type codexSession struct {
	conn           *codexRPC
	threadID       string
	turnID         string
	mu             sync.Mutex
	providerTurnID string
}

func (s *codexSession) Steer(ctx context.Context, turnID, text string) error {
	if turnID != s.turnID || strings.TrimSpace(text) == "" {
		return errors.New("the steer does not target this Codex turn")
	}
	s.mu.Lock()
	providerID := s.providerTurnID
	s.mu.Unlock()
	if providerID == "" {
		return errors.New("Codex has not started the turn yet")
	}
	var result struct {
		TurnID string `json:"turnId"`
	}
	err := s.conn.request(ctx, "turn/steer", map[string]any{
		"threadId":       s.threadID,
		"expectedTurnId": providerID,
		"input":          []map[string]any{{"type": "text", "text": text}},
	}, &result)
	if err != nil {
		return err
	}
	if result.TurnID != "" && result.TurnID != providerID {
		return errors.New("Codex steered a different turn")
	}
	return nil
}

// Codex may finish a turn without a completion notification reaching this
// connection. Its durable thread state is authoritative for that exact turn.
func readCodexTurnStatus(ctx context.Context, conn *codexRPC, threadID, turnID string) (string, error) {
	var result struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := conn.request(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result); err != nil {
		return "", err
	}
	if result.Thread.ID != threadID {
		return "", errors.New("Codex returned a different thread")
	}
	for _, providerTurn := range result.Thread.Turns {
		if providerTurn.ID == turnID {
			return providerTurn.Status, nil
		}
	}
	return "", nil
}

func (s *Supervisor) runCodex(ctx context.Context, turn worker.Turn, command Command, publish func(Output) error, identity func(string) error) error {
	control, ok := s.Control.(approvalControl)
	if !ok {
		return errors.New("Cloud approval control is unavailable")
	}
	process := exec.CommandContext(ctx, command.Path, "app-server")
	configureProviderProcess(process)
	process.Dir = command.Dir
	process.Env = mergedEnvironment(command.Env)
	stdin, err := process.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr lockedBuffer
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() { _ = stopProviderProcess(process); _ = process.Wait() }()
	conn := newCodexRPC(stdin, stdout)
	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := conn.request(openCtx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "ao-cloud", "title": "AO Cloud", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		return fmt.Errorf("initialize Codex: %w: %s", err, boundedError(stderr.String()))
	}
	if err := conn.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	policy, sandbox, reviewer := codexApprovalSettings(turn)
	threadParams := map[string]any{
		"cwd": command.Dir, "approvalPolicy": policy, "approvalsReviewer": reviewer, "sandbox": sandbox,
	}
	if command.SystemPrompt != "" {
		threadParams["developerInstructions"] = command.SystemPrompt
	}
	if turn.Model != "" {
		threadParams["model"] = turn.Model
	}
	if turn.ReasoningEffort != "" {
		threadParams["config"] = map[string]string{"model_reasoning_effort": turn.ReasoningEffort}
	}
	threadID := turn.AgentSessionID
	if threadID != "" {
		threadParams["threadId"] = threadID
		if err := conn.request(openCtx, "thread/resume", threadParams, nil); err != nil {
			return fmt.Errorf("resume Codex thread: %w", err)
		}
	} else {
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := conn.request(openCtx, "thread/start", threadParams, &result); err != nil {
			return fmt.Errorf("start Codex thread: %w", err)
		}
		threadID = result.Thread.ID
		if threadID == "" {
			return errors.New("Codex returned no thread id")
		}
		if err := identity(threadID); err != nil {
			return err
		}
	}
	session := &codexSession{conn: conn, threadID: threadID, turnID: turn.ID}
	completed := make(chan error, 1)
	conn.mu.Lock()
	conn.onNotification = func(frame codexFrame) {
		switch frame.Method {
		case "turn/started":
			var event struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(frame.Params, &event) == nil && event.ThreadID == threadID {
				session.mu.Lock()
				session.providerTurnID = event.Turn.ID
				session.mu.Unlock()
			}
		case "item/agentMessage/delta":
			var event struct {
				ThreadID string `json:"threadId"`
				Delta    string `json:"delta"`
			}
			if json.Unmarshal(frame.Params, &event) == nil && event.Delta != "" && (event.ThreadID == "" || event.ThreadID == threadID) {
				if err := publish(Output{Stream: "stdout", Text: event.Delta}); err != nil {
					select {
					case completed <- err:
					default:
					}
				}
			}
		case "turn/completed":
			var event struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(frame.Params, &event) == nil && event.ThreadID == threadID {
				var result error
				if event.Turn.Status != "completed" {
					result = fmt.Errorf("Codex turn ended with %s", event.Turn.Status)
				}
				select {
				case completed <- result:
				default:
				}
			}
		}
	}
	conn.onRequest = func(frame codexFrame) { handleCodexApproval(ctx, conn, control, turn, frame) }
	conn.mu.Unlock()
	turnParams := map[string]any{
		"threadId": threadID, "input": []map[string]any{{"type": "text", "text": turn.Prompt}},
		"approvalPolicy": policy, "approvalsReviewer": reviewer,
		"sandboxPolicy": codexTurnSandbox(sandbox),
	}
	if turn.Model != "" {
		turnParams["model"] = turn.Model
	}
	if turn.ReasoningEffort != "" {
		turnParams["effort"] = turn.ReasoningEffort
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := conn.request(ctx, "turn/start", turnParams, &started); err != nil {
		return fmt.Errorf("start Codex turn: %w", err)
	}
	if started.Turn.ID == "" {
		return errors.New("Codex returned no turn id")
	}
	s.activeMu.Lock()
	s.activeCodex = session
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		if s.activeCodex == session {
			s.activeCodex = nil
		}
		s.activeMu.Unlock()
	}()
	if publisher, ok := s.Control.(capabilityPublisher); ok {
		if err := publisher.PublishTurnCapabilities(ctx, turn.ID, turn.Attempt, true); err != nil {
			return err
		}
	}
	completionPoll := time.NewTicker(2 * time.Second)
	defer completionPoll.Stop()
	for {
		select {
		case err := <-completed:
			return err
		case <-completionPoll.C:
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			status, readErr := readCodexTurnStatus(readCtx, conn, threadID, started.Turn.ID)
			cancel()
			if readErr != nil {
				continue
			}
			switch status {
			case "completed":
				return nil
			case "failed", "interrupted":
				return fmt.Errorf("Codex turn ended with %s", status)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.done:
			return errors.New("Codex app-server disconnected before completing the turn")
		}
	}
}

func codexApprovalSettings(turn worker.Turn) (policy, sandbox, reviewer string) {
	reviewer = "user"
	if turn.Mode == "read-only" {
		return "never", "read-only", reviewer
	}
	approval := turn.ApprovalMode
	if approval == "" {
		if turn.Mode == "standard" {
			approval = "auto"
		} else {
			approval = "default"
		}
	}
	switch approval {
	case "accept-edits":
		return "on-request", "workspace-write", reviewer
	case "auto":
		return "on-request", "workspace-write", "auto_review"
	default:
		return "never", "danger-full-access", reviewer
	}
}

func codexTurnSandbox(sandbox string) map[string]string {
	switch sandbox {
	case "read-only":
		return map[string]string{"type": "readOnly"}
	case "workspace-write":
		return map[string]string{"type": "workspaceWrite"}
	default:
		return map[string]string{"type": "dangerFullAccess"}
	}
}

func handleCodexApproval(ctx context.Context, conn *codexRPC, control approvalControl, turn worker.Turn, frame codexFrame) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if frame.Method != "item/commandExecution/requestApproval" && frame.Method != "item/fileChange/requestApproval" && frame.Method != "item/permissions/requestApproval" {
		_ = conn.send(map[string]any{"id": frame.ID, "error": map[string]any{"code": -32601, "message": "unsupported request"}})
		return
	}
	var request struct {
		Command            string            `json:"command"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if json.Unmarshal(frame.Params, &request) != nil || len(request.AvailableDecisions) == 0 {
		_ = conn.send(map[string]any{"id": frame.ID, "error": map[string]any{"code": -32602, "message": "invalid approval choices"}})
		return
	}
	options := make([]map[string]string, 0, len(request.AvailableDecisions))
	offered := make(map[string]json.RawMessage)
	for _, raw := range request.AvailableDecisions {
		id := ""
		if json.Unmarshal(raw, &id) != nil {
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
				continue
			}
			for key := range object {
				id = key
			}
		}
		if id == "" {
			continue
		}
		offered[id] = raw
		kind := ""
		if strings.HasPrefix(id, "accept") {
			kind = "allow_once"
		}
		if id == "acceptForSession" {
			kind = "allow_always"
		}
		if id == "decline" || id == "cancel" {
			kind = "reject_once"
		}
		options = append(options, map[string]string{"id": id, "label": id, "kind": kind})
	}
	if len(options) == 0 {
		_ = conn.send(map[string]any{"id": frame.ID, "error": map[string]any{"code": -32602, "message": "no supported approval choices"}})
		return
	}
	encoded, _ := json.Marshal(options)
	summary := "Apply file changes"
	if request.Command != "" {
		summary = "Run " + request.Command
	}
	if len(summary) > 4096 {
		summary = summary[:4096]
	}
	requestID := uuid.NewString()
	if err := control.CreateChatApproval(ctx, worker.ChatApproval{RequestID: requestID, TurnID: turn.ID, Attempt: turn.Attempt, Summary: summary, Decisions: encoded}); err != nil {
		_ = conn.send(map[string]any{"id": frame.ID, "error": map[string]any{"code": -32000, "message": "approval could not be recorded"}})
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		decision, err := control.ChatApprovalDecision(ctx, turn.ID, turn.Attempt, requestID)
		if err != nil {
			_ = conn.send(map[string]any{"id": frame.ID, "error": map[string]any{"code": -32000, "message": "approval decision unavailable"}})
			return
		}
		if raw, ok := offered[decision]; ok {
			_ = conn.send(map[string]any{"id": frame.ID, "result": map[string]any{"decision": raw}})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
