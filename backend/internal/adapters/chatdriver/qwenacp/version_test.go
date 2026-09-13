package qwenacp

import "testing"

func TestValidateVersionOutputAcceptsNativeACPMinimum(t *testing.T) {
	for _, output := range []string{"0.15.0", "0.23.0", "@qwen-code/qwen-code 0.15.0"} {
		if err := validateVersionOutput(output); err != nil {
			t.Errorf("validateVersionOutput(%q): %v", output, err)
		}
	}
}

func TestValidateVersionOutputRejectsBuildsWithoutNativeACP(t *testing.T) {
	for _, output := range []string{"0.14.9", "0.9.0", "unknown"} {
		if err := validateVersionOutput(output); err == nil {
			t.Errorf("validateVersionOutput(%q) = nil, want incompatible version", output)
		}
	}
}
