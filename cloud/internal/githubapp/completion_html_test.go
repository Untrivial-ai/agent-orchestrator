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

func TestCompletionHTMLUsesPlainCallbackLayout(t *testing.T) {
	service := &Service{}
	for _, success := range []bool{true, false} {
		page := string(service.CompletionHTML(success))
		if !strings.Contains(page, "<h1") || !strings.Contains(page, "<p") {
			t.Errorf("success=%t: completion page needs a heading and message", success)
		}
		for _, unwanted := range []string{"<style", "class=", "<section", "<div"} {
			if strings.Contains(page, unwanted) {
				t.Errorf("success=%t: completion page contains decorative markup %q", success, unwanted)
			}
		}
	}
}
