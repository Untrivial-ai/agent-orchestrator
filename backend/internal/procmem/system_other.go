//go:build !linux && !darwin && !windows

package procmem

// ReadSystem has no reader for this platform, so the machine bar and the CPU
// graph are absent rather than wrong. Session readings are unaffected.
func ReadSystem() (System, error) { return System{}, ErrUnsupported }
