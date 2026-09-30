package browser

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeSessions struct {
	session domain.Session
	err     error
}

func (f fakeSessions) Get(_ context.Context, _ domain.SessionID) (domain.Session, error) {
	return f.session, f.err
}

type fakeRuntime struct {
	action  string
	surface string
}

func (f *fakeRuntime) Status(_ domain.SessionID, surface string) (browserruntime.Status, string) {
	f.surface = surface
	return browserruntime.Status{Connected: true}, surface
}

func (f *fakeRuntime) Execute(
	_ context.Context,
	_ domain.SessionID,
	surface string,
	action string,
	_ map[string]interface{},
) (browserruntime.Result, error) {
	f.action, f.surface = action, surface
	return browserruntime.Result{RequestID: "r1"}, nil
}

func TestServiceRequiresOwningCapabilityAndLiveSession(t *testing.T) {
	authority := NewAuthority()
	token, verifier, err := authority.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	service := New(fakeSessions{session: domain.Session{SessionRecord: domain.SessionRecord{
		ID:       "s1",
		Metadata: domain.SessionMetadata{BrowserCapabilityVerifier: verifier},
	}}}, runtime, authority)

	if _, _, err := service.Status(context.Background(), "s1", "wrong", "auto"); apiErrorCode(err) != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("wrong capability error = %v", err)
	}
	if _, _, err := service.Status(context.Background(), "s1", token, "mobile"); err != nil {
		t.Fatalf("valid capability: %v", err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "mobile", " SNAPSHOT ", nil); err != nil || action != "snapshot" || runtime.action != "snapshot" || runtime.surface != "mobile" {
		t.Fatalf("execute action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "auto", "dblclick", nil); err != nil || action != "dblclick" || runtime.action != "dblclick" {
		t.Fatalf("expanded action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "auto", "act", nil); err != nil || action != "act" || runtime.action != "act" {
		t.Fatalf("act action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "desktop", "DEVTOOLS-OPEN", nil); err != nil || action != "devtools-open" || runtime.action != "devtools-open" {
		t.Fatalf("devtools action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	for _, action := range []string{"devtools-toggle", "devtools-focus"} {
		if _, _, err := service.Execute(context.Background(), "s1", token, "auto", action, nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
			t.Fatalf("agent-facing %s error = %v", action, err)
		}
	}
	if _, _, err := service.Execute(context.Background(), "s1", token, "auto", "agent-browser-run", nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
		t.Fatalf("removed nested action error = %v", err)
	}
	if _, _, err := service.Execute(context.Background(), "s1", token, "auto", "eval", nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
		t.Fatalf("unsupported action error = %v", err)
	}

	terminated := New(
		fakeSessions{session: domain.Session{SessionRecord: domain.SessionRecord{ID: "s1", IsTerminated: true}}},
		runtime,
		authority,
	)
	if _, _, err := terminated.Status(context.Background(), "s1", token, "auto"); apiErrorCode(err) != "SESSION_TERMINATED" {
		t.Fatalf("terminated error = %v", err)
	}
}

func TestAuthorityUsesLaunchScopedSessionSecrets(t *testing.T) {
	authority := NewAuthority()
	firstToken, firstVerifier, err := authority.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, secondVerifier, err := authority.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := authority.Issue("s2")
	if err != nil {
		t.Fatal(err)
	}
	if firstToken == "" || firstToken == secondToken || firstToken == otherToken || firstVerifier == secondVerifier {
		t.Fatal("issued capabilities are not random and launch-scoped")
	}
}

func TestServiceRotationRejectsOldBearerAndDispatchesNewBearer(t *testing.T) {
	authority := NewAuthority()
	oldToken, _, err := authority.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	newToken, newVerifier, err := authority.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	service := New(fakeSessions{session: domain.Session{SessionRecord: domain.SessionRecord{
		ID:       "s1",
		Metadata: domain.SessionMetadata{BrowserCapabilityVerifier: newVerifier},
	}}}, runtime, authority)
	if _, _, err := service.Execute(context.Background(), "s1", oldToken, "auto", "snapshot", nil); apiErrorCode(err) != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("old bearer error = %v, want BROWSER_CAPABILITY_INVALID", err)
	}
	if _, _, err := service.Execute(context.Background(), "s1", newToken, "auto", "snapshot", nil); err != nil {
		t.Fatalf("new bearer execute: %v", err)
	}
	if runtime.action != "snapshot" {
		t.Fatalf("browser runtime action = %q, want snapshot", runtime.action)
	}
}

func TestAuthorityValidatesDurableVerifierAcrossDaemonReplacement(t *testing.T) {
	first := NewAuthority()
	token, verifier, err := first.Issue("s1")
	if err != nil {
		t.Fatal(err)
	}
	replacement := NewAuthority()
	if !replacement.Valid("s1", token, verifier) {
		t.Fatal("replacement daemon rejected the surviving worker capability")
	}
	if replacement.Valid("s2", token, verifier) || replacement.Valid("s1", verifier, verifier) {
		t.Fatal("verifier authorized a different session or worked as a bearer token")
	}
}

func apiErrorCode(err error) string {
	var target *apierr.Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
