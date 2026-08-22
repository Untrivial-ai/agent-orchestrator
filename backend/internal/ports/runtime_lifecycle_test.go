package ports

import (
	"errors"
	"testing"
)

func TestRuntimeCreateFailureOf(t *testing.T) {
	cause := errors.New("boom")
	handle := RuntimeHandle{ID: "session-1", RuntimeLaunchID: "launch-1"}

	tests := []struct {
		name            string
		err             error
		wantDisposition RuntimeCreateDisposition
		wantHandle      RuntimeHandle
	}{
		{name: "rollback safe", err: NewRuntimeCreateRollbackSafeError(cause), wantDisposition: RuntimeCreateRollbackSafe},
		{name: "preserve", err: NewRuntimeCreatePreserveError(handle, cause), wantDisposition: RuntimeCreatePreserve, wantHandle: handle},
		{name: "untyped fails closed", err: cause, wantDisposition: RuntimeCreatePreserve},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disposition, gotHandle := RuntimeCreateFailureOf(tt.err)
			if disposition != tt.wantDisposition || gotHandle != tt.wantHandle {
				t.Fatalf("RuntimeCreateFailureOf() = (%q, %#v), want (%q, %#v)", disposition, gotHandle, tt.wantDisposition, tt.wantHandle)
			}
			if !errors.Is(tt.err, cause) {
				t.Fatalf("wrapped error does not preserve cause: %v", tt.err)
			}
		})
	}
}
