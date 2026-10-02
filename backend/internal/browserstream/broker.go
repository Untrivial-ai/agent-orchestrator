package browserstream

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

const helloTimeout = 5 * time.Second

// ErrUnavailable means the Electron browser host cannot currently serve a stream.
var ErrUnavailable = errors.New("browser stream host is unavailable")

// Event is a frame or state transition emitted by the Electron browser host.
type Event struct {
	Type    string
	Frame   *Frame
	Payload json.RawMessage
	Code    string
	Message string
}

type streamState struct {
	id        uint32
	sessionID string
	subs      map[uint64]chan Event
}

// Subscription receives a latest-value stream for one AO session.
type Subscription struct {
	StreamID uint32
	Events   <-chan Event
	close    func()
}

// Close releases this subscriber and stops the host capture when it was the last one.
func (s *Subscription) Close() {
	if s != nil && s.close != nil {
		s.close()
		s.close = nil
	}
}

// Broker owns the single authenticated Electron media link and fans one host
// capture out to any number of daemon-side subscribers.
type Broker struct {
	log   *slog.Logger
	token string

	mu        sync.Mutex
	conn      net.Conn
	writeMu   sync.Mutex
	streams   map[string]*streamState
	byID      map[uint32]*streamState
	nextID    uint32
	nextSubID uint64
}

// New creates a broker authenticated with the private daemon-to-Electron token.
func New(log *slog.Logger, token string) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{log: log, token: token, streams: map[string]*streamState{}, byID: map[uint32]*streamState{}, nextID: 1}
}

// Connected reports whether an authenticated Electron browser host is attached.
func (b *Broker) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil
}

// Subscribe starts or joins the latest-value frame stream for one AO session.
func (b *Broker) Subscribe(ctx context.Context, sessionID string) (*Subscription, error) {
	if sessionID == "" {
		return nil, errors.New("browser stream session id is required")
	}
	b.mu.Lock()
	if b.conn == nil {
		b.mu.Unlock()
		return nil, ErrUnavailable
	}
	stream := b.streams[sessionID]
	first := stream == nil
	if first {
		stream = &streamState{id: b.nextID, sessionID: sessionID, subs: map[uint64]chan Event{}}
		b.nextID++
		b.streams[sessionID] = stream
		b.byID[stream.id] = stream
	}
	b.nextSubID++
	subID := b.nextSubID
	ch := make(chan Event, 1)
	stream.subs[subID] = ch
	streamID := stream.id
	b.mu.Unlock()

	if first {
		if err := b.send(ctx, Control{Type: "start", StreamID: streamID, SessionID: sessionID}); err != nil {
			b.unsubscribe(sessionID, subID)
			return nil, err
		}
	}
	sub := &Subscription{StreamID: streamID, Events: ch}
	sub.close = func() { b.unsubscribe(sessionID, subID) }
	return sub, nil
}

func (b *Broker) unsubscribe(sessionID string, subID uint64) {
	b.mu.Lock()
	stream := b.streams[sessionID]
	if stream == nil {
		b.mu.Unlock()
		return
	}
	ch, ok := stream.subs[subID]
	if ok {
		delete(stream.subs, subID)
		close(ch)
	}
	last := len(stream.subs) == 0
	if last {
		delete(b.streams, sessionID)
		delete(b.byID, stream.id)
	}
	conn := b.conn
	b.mu.Unlock()
	if last && conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = b.send(ctx, Control{Type: "stop", StreamID: stream.id, SessionID: sessionID})
		cancel()
	}
}

// Send delivers a validated input/navigation/lease command to the host.
func (b *Broker) Send(ctx context.Context, sessionID, typ string, payload any) error {
	b.mu.Lock()
	stream := b.streams[sessionID]
	b.mu.Unlock()
	if stream == nil {
		return ErrUnavailable
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return b.send(ctx, Control{Type: typ, StreamID: stream.id, SessionID: sessionID, Payload: raw})
}

func (b *Broker) send(ctx context.Context, control Control) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return ErrUnavailable
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
	err := writeControl(conn, control)
	_ = conn.SetWriteDeadline(time.Time{})
	if err != nil {
		b.disconnect(conn, err)
		return ErrUnavailable
	}
	return nil
}

// Serve accepts the single private browser-host connection until ctx is canceled.
func (b *Broker) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // Closing the listener is the normal Serve shutdown path.
			}
			return err
		}
		go b.serveConn(ctx, conn)
	}
}

func (b *Broker) serveConn(ctx context.Context, conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	kind, body, err := readPacket(conn)
	if err != nil || kind != KindControl {
		_ = conn.Close()
		return
	}
	hello, err := decodeControl(body)
	if err != nil || hello.Type != "hello" || hello.Version != ProtocolVersion || !validToken(b.token, hello.Token) {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	b.mu.Lock()
	old := b.conn
	b.conn = conn
	streams := make([]*streamState, 0, len(b.streams))
	for _, stream := range b.streams {
		streams = append(streams, stream)
	}
	b.mu.Unlock()
	if old != nil && old != conn {
		_ = old.Close()
	}
	b.log.Info("browser stream host connected")
	for _, stream := range streams {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		_ = b.send(ctx, Control{Type: "start", StreamID: stream.id, SessionID: stream.sessionID})
		cancel()
	}

	for {
		kind, body, err = readPacket(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				b.log.Warn("browser stream host read failed", "err", err)
			}
			b.disconnect(conn, err)
			return
		}
		switch kind {
		case KindJPEG:
			frame, decodeErr := decodeFrame(body)
			if decodeErr != nil {
				b.disconnect(conn, decodeErr)
				return
			}
			b.publish(frame.StreamID, Event{Type: "frame", Frame: &frame})
		case KindControl:
			control, decodeErr := decodeControl(body)
			if decodeErr != nil {
				b.disconnect(conn, decodeErr)
				return
			}
			b.publish(control.StreamID, Event{Type: control.Type, Payload: control.Payload, Code: control.Code, Message: control.Message})
		default:
			b.disconnect(conn, fmt.Errorf("unknown browser stream packet kind %d", kind))
			return
		}
	}
}

func (b *Broker) publish(streamID uint32, event Event) {
	b.mu.Lock()
	stream := b.byID[streamID]
	if stream == nil {
		b.mu.Unlock()
		return
	}
	for _, ch := range stream.subs {
		select {
		case ch <- event:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- event:
			default:
			}
		}
	}
	b.mu.Unlock()
}

func (b *Broker) disconnect(conn net.Conn, cause error) {
	b.mu.Lock()
	if b.conn != conn {
		b.mu.Unlock()
		return
	}
	b.conn = nil
	streams := make([]*streamState, 0, len(b.streams))
	for _, stream := range b.streams {
		streams = append(streams, stream)
	}
	b.mu.Unlock()
	_ = conn.Close()
	for _, stream := range streams {
		b.publish(stream.id, Event{Type: "error", Code: "BROWSER_HOST_UNAVAILABLE", Message: "Desktop browser host disconnected"})
	}
	if cause != nil {
		b.log.Info("browser stream host disconnected", "err", cause)
	}
}

func validToken(expected, supplied string) bool {
	return expected != "" && supplied != "" && hmac.Equal([]byte(expected), []byte(supplied))
}
