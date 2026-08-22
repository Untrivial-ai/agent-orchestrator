package ports

import "errors"

// RuntimeCreateDisposition tells a lifecycle owner whether a failed Create
// permits rollback of resources outside the runtime adapter.
type RuntimeCreateDisposition string

const (
	// RuntimeCreateRollbackSafe means the requested runtime generation is
	// absent or has been authoritatively released.
	RuntimeCreateRollbackSafe RuntimeCreateDisposition = "rollback_safe"
	// RuntimeCreatePreserve means a runtime generation may remain or ownership
	// could not be proven. Callers must preserve its session and workspace.
	RuntimeCreatePreserve RuntimeCreateDisposition = "preserve"
)

type runtimeCreateError struct {
	disposition RuntimeCreateDisposition
	handle      RuntimeHandle
	err         error
}

func (e *runtimeCreateError) Error() string { return e.err.Error() }
func (e *runtimeCreateError) Unwrap() error { return e.err }

// NewRuntimeCreateRollbackSafeError classifies err as a Create failure after
// which the caller may roll back its workspace.
//
// Example: an adapter rejects invalid launch arguments before starting any
// external process and returns this wrapper around the validation error.
func NewRuntimeCreateRollbackSafeError(err error) error {
	if err == nil {
		return nil
	}
	return &runtimeCreateError{disposition: RuntimeCreateRollbackSafe, err: err}
}

// NewRuntimeCreatePreserveError classifies err as a Create failure whose exact
// runtime reference must remain available for later cleanup.
//
// Example: a canceled tmux client may have created the requested generation,
// so the adapter returns its requested handle and this preserve disposition.
func NewRuntimeCreatePreserveError(handle RuntimeHandle, err error) error {
	if err == nil {
		return nil
	}
	return &runtimeCreateError{disposition: RuntimeCreatePreserve, handle: handle, err: err}
}

// RuntimeCreateFailureOf returns the rollback decision and exact reference for
// a failed Runtime.Create. Untyped errors are preserve-required: an unknown
// adapter result must never authorize workspace deletion.
//
// Example: a lifecycle owner calls this after Create returns an error, rolls
// back only for RuntimeCreateRollbackSafe, and otherwise stores the handle.
func RuntimeCreateFailureOf(err error) (RuntimeCreateDisposition, RuntimeHandle) {
	var failure *runtimeCreateError
	if errors.As(err, &failure) {
		return failure.disposition, failure.handle
	}
	return RuntimeCreatePreserve, RuntimeHandle{}
}
