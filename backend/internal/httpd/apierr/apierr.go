// Package apierr defines the REST API's error vocabulary: a single structured
// error type every service returns and the controllers render into the locked
// APIError envelope with one errors.As. It is deliberately scoped to the HTTP
// API tree — these services exist to serve the daemon's REST surface — and
// imports nothing, so any layer may depend on it without an import cycle.
package apierr

// Kind is a semantic failure category. It is not an HTTP status or word: the
// envelope layer is the only place a Kind is translated into one.
type Kind int

const (
	// KindInternal is an unexpected failure; it maps to 500. As iota's zero
	// value it is also the Kind of a zero-value Error, so an Error built without
	// a Kind safely defaults to a 500.
	KindInternal Kind = iota
	// KindInvalid is malformed or rejected input; it maps to 400.
	KindInvalid
	// KindNotFound is a missing resource; it maps to 404.
	KindNotFound
	// KindConflict is a state/uniqueness clash; it maps to 409.
	KindConflict
	// KindForbidden is an authenticated ownership failure; it maps to 403.
	KindForbidden
	// KindTooManyRequests is a bounded local resource limit; it maps to 429.
	KindTooManyRequests
	// KindNotImplemented is a capability the installed provider does not expose.
	KindNotImplemented
	// KindUnavailable is a transient capability or local resource failure the
	// caller should retry (for example, a temporarily busy database); it maps to
	// 503 rather than reporting the contention as an internal server fault.
	KindUnavailable
)

// Error is the structured error every service returns. Code is a stable machine
// identifier (e.g. "SESSION_NOT_FOUND"); Message is the human-facing text. It
// reaches the controller through fmt.Errorf("...: %w", err) wrapping and is
// matched there with errors.As.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// New builds an Error from its parts.
func New(kind Kind, code, message string, details map[string]any) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Details: details}
}

// Invalid is a 400-class error.
func Invalid(code, message string, details map[string]any) *Error {
	return New(KindInvalid, code, message, details)
}

// NotFound is a 404-class error.
func NotFound(code, message string) *Error {
	return New(KindNotFound, code, message, nil)
}

// Conflict is a 409-class error.
func Conflict(code, message string, details map[string]any) *Error {
	return New(KindConflict, code, message, details)
}

// Forbidden is a 403-class ownership failure.
func Forbidden(code, message string) *Error {
	return New(KindForbidden, code, message, nil)
}

// TooManyRequests is a 429-class local concurrency limit.
func TooManyRequests(code, message string) *Error {
	return New(KindTooManyRequests, code, message, nil)
}

// NotImplemented is a 501-class provider capability failure.
func NotImplemented(code, message string) *Error {
	return New(KindNotImplemented, code, message, nil)
}

// Unavailable is a 503-class transient local capability failure.
func Unavailable(code, message string) *Error {
	return New(KindUnavailable, code, message, nil)
}

// Internal is a 500-class error.
func Internal(code, message string) *Error {
	return New(KindInternal, code, message, nil)
}
