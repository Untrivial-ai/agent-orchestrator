package githubapp

import (
	"strings"
	"testing"
)

func TestInstallationCompletionHTMLExplainsNextStep(t *testing.T) {
	service := &Service{}
	page := string(service.InstallationCompletionHTML(true))
	for _, want := range []string{
		"GitHub connected",
		"Return to AO",
		"repositories",
		"<main",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("completion page missing %q", want)
		}
	}
	if strings.Contains(page, "Connection failed") {
		t.Fatal("success page contains failure message")
	}
}
