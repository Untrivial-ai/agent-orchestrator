// Package ownership attaches an observability reporting owner to an error
// without changing the error's identity or presentation. Ownership survives
// ordinary wrapping and can be copied when a boundary maps one error to
// another.
package ownership

// Owner identifies the layer responsible for reporting an error to Sentry.
type Owner string

// Owner values identify the layer responsible for reporting a failure.
const (
	OwnerHTTP            Owner = "http"
	OwnerAgentSwitchSaga Owner = "agent_switch_saga"
)

// OwnedError wraps an error with its observability reporting owner.
type OwnedError struct {
	Err   error
	Owner Owner
}

func (e *OwnedError) Error() string { return e.Err.Error() }

// Unwrap preserves errors.Is and errors.As behavior through the annotation.
func (e *OwnedError) Unwrap() error { return e.Err }

// ObservabilityOwner exposes the annotation through wrapped error chains.
func (e *OwnedError) ObservabilityOwner() Owner { return e.Owner }

type ownerCarrier interface {
	ObservabilityOwner() Owner
}

// Own annotates err unless it already has a valid reporting owner. Invalid
// owners are ignored so untrusted values cannot cross the wire.
func Own(err error, owner Owner) error {
	if err == nil || !owner.Valid() || OwnerOf(err).Valid() {
		return err
	}
	return &OwnedError{Err: err, Owner: owner}
}

// OwnerOf returns the first valid reporting owner in err's wrapping tree.
func OwnerOf(err error) Owner {
	if err == nil {
		return ""
	}
	if carrier, ok := err.(ownerCarrier); ok {
		owner := carrier.ObservabilityOwner()
		if owner.Valid() {
			return owner
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if owner := OwnerOf(child); owner.Valid() {
				return owner
			}
		}
		return ""
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return OwnerOf(wrapped.Unwrap())
	}
	return ""
}

// Preserve copies original's validated ownership onto mapped. It deliberately
// does not retain original's error chain, and it never replaces mapped's owner.
func Preserve(original, mapped error) error {
	if mapped == nil {
		return nil
	}
	return Own(mapped, OwnerOf(original))
}

// Valid reports whether owner is one of the closed wire-safe values.
func (owner Owner) Valid() bool {
	return owner == OwnerHTTP || owner == OwnerAgentSwitchSaga
}
