//go:build !windows

package providersecret

import "errors"

type unsupported struct{}

func New() Protector { return unsupported{} }
func (unsupported) Protect([]byte) ([]byte, error) {
	return nil, errors.New("provider secret storage requires Windows DPAPI")
}
func (unsupported) Unprotect([]byte) ([]byte, error) {
	return nil, errors.New("provider secret storage requires Windows DPAPI")
}
