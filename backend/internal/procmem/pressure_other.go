//go:build !darwin

package procmem

// ReadPressure is ReadSystem's pressure fields. Linux reads a few /proc files
// and Windows makes two API calls, so the full reading is already cheap.
func ReadPressure() (Pressure, error) {
	sys, err := ReadSystem()
	if err != nil {
		return Pressure{}, err
	}
	return Pressure{Raw: sys.PressureRaw, Source: sys.PressureSource}, nil
}
