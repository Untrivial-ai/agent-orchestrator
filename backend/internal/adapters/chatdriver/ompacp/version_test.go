package ompacp

import "testing"

func TestValidateVersionOutputAcceptsNativeACPMinimum(t *testing.T) {
	for _, output := range []string{"omp/15.0.0", "omp/17.3.5", "omp 18.0.0"} {
		if err := validateVersionOutput(output); err != nil {
			t.Errorf("validateVersionOutput(%q): %v", output, err)
		}
	}
}

func TestValidateVersionOutputRejectsBuildsWithoutNativeACP(t *testing.T) {
	for _, output := range []string{"omp/14.9.9", "omp/3.15.1", "unknown"} {
		if err := validateVersionOutput(output); err == nil {
			t.Errorf("validateVersionOutput(%q) = nil, want incompatible version", output)
		}
	}
}
