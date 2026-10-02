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

func TestInstallationConflictHTMLNamesAccountAndStaysPlain(t *testing.T) {
	service := &Service{}
	page := string(service.InstallationConflictHTML("octo-org"))
	for _, want := range []string{
		"octo-org",
		"already connected to another AO workspace",
		"connect a different GitHub account",
		"<h1",
		"<p",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("conflict page missing %q", want)
		}
	}
	for _, unwanted := range []string{"<style", "class=", "<section", "<div"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("conflict page contains decorative markup %q", unwanted)
		}
	}
	if strings.Contains(page, "Connection failed") {
		t.Error("conflict page must not read as a generic failure")
	}
}

func TestInstallationConflictHTMLEscapesAccountLoginAndHandlesEmpty(t *testing.T) {
	service := &Service{}
	page := string(service.InstallationConflictHTML(`<script>alert(1)</script>`))
	if strings.Contains(page, "<script>") {
		t.Fatal("account login was not HTML-escaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("expected the account login to be HTML-escaped")
	}
	empty := string(service.InstallationConflictHTML(""))
	if !strings.Contains(empty, "This GitHub account is already connected to another AO workspace") {
		t.Fatalf("empty-account conflict page = %q", empty)
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
