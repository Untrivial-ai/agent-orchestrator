package browserstream

import (
	"context"
	"sync"
)

// Latest retains at most one disposable frame. Put never blocks; a newer frame
// replaces an older unsent frame and reports that replacement to the caller.
type Latest struct {
	mu     sync.Mutex
	frame  []byte
	wake   chan struct{}
	closed chan struct{}
	once   sync.Once
}

func NewLatest() *Latest {
	return &Latest{wake: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (l *Latest) Put(frame []byte) (replaced bool) {
	copyFrame := append([]byte(nil), frame...)
	l.mu.Lock()
	replaced = len(l.frame) > 0
	l.frame = copyFrame
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return replaced
}

func (l *Latest) Next(ctx context.Context) ([]byte, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-l.closed:
			return nil, false
		case <-l.wake:
			l.mu.Lock()
			frame := l.frame
			l.frame = nil
			l.mu.Unlock()
			if len(frame) > 0 {
				return frame, true
			}
		}
	}
}

func (l *Latest) Close() {
	l.once.Do(func() { close(l.closed) })
}
