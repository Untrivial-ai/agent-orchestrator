package vmbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestCDPEventBurstDoesNotBlockReplies(t *testing.T) {
	for _, count := range []int{32, maxCDPQueuedEvents + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				_, payload, err := conn.Read(ctx)
				if err != nil {
					return
				}
				var command struct {
					ID int64 `json:"id"`
				}
				if json.Unmarshal(payload, &command) != nil {
					return
				}
				for range count {
					if conn.Write(ctx, websocket.MessageText, []byte(`{"method":"Target.targetCreated","params":{}}`)) != nil {
						return
					}
				}
				reply, _ := json.Marshal(map[string]any{"id": command.ID, "result": map[string]bool{"ok": true}})
				_ = conn.Write(ctx, websocket.MessageText, reply)
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := dialWebsocketCDP(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var result struct {
				OK bool `json:"ok"`
			}
			err = client.Call(ctx, "", "Target.setDiscoverTargets", nil, &result)
			if count <= maxCDPQueuedEvents {
				if err != nil || !result.OK {
					t.Fatalf("reply=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, errCDPEventOverflow) {
				t.Fatalf("overflow error=%v", err)
			}
		})
	}
}

func TestCDPCallCancellationAndConcurrentClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			if conn.Write(r.Context(), websocket.MessageText, []byte(`{"method":"Target.targetCreated"}`)) != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}))
	defer server.Close()
	client, err := dialWebsocketCDP(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.Call(ctx, "", "NeverReplies", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", err)
	}
	_ = client.Close()
	for range client.Events() {
	}
}
