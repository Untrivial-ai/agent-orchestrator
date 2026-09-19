package qwenacp

import "testing"

func TestValidateVersionOutputAcceptsAutoApprovalMinimum(t *testing.T) {
	for _, output := range []string{"0.16.0", "0.23.0", "@qwen-code/qwen-code 0.16.0"} {
		if err := validateVersionOutput(output); err != nil {
			t.Errorf("validateVersionOutput(%q): %v", output, err)
		}
	}
}

func TestValidateVersionOutputRejectsBuildsWithoutAutoApproval(t *testing.T) {
	for _, output := range []string{"0.15.12", "0.14.9", "unknown"} {
		if err := validateVersionOutput(output); err == nil {
			t.Errorf("validateVersionOutput(%q) = nil, want incompatible version", output)
		}
	}
}
