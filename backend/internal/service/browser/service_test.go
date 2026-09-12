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
	action string
}

func (f *fakeRuntime) Status() browserruntime.Status {
	return browserruntime.Status{Connected: true}
}

func (f *fakeRuntime) Execute(
	_ context.Context,
	_ domain.SessionID,
	action string,
	_ map[string]interface{},
) (browserruntime.Result, error) {
	f.action = action
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

	if _, err := service.Status(context.Background(), "s1", "wrong"); apiErrorCode(err) != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("wrong capability error = %v", err)
	}
	if _, err := service.Status(context.Background(), "s1", token); err != nil {
		t.Fatalf("valid capability: %v", err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, " SNAPSHOT ", nil); err != nil || action != "snapshot" || runtime.action != "snapshot" {
		t.Fatalf("execute action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "dblclick", nil); err != nil || action != "dblclick" || runtime.action != "dblclick" {
		t.Fatalf("expanded action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "act", nil); err != nil || action != "act" || runtime.action != "act" {
		t.Fatalf("act action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	if _, action, err := service.Execute(context.Background(), "s1", token, "DEVTOOLS-OPEN", nil); err != nil || action != "devtools-open" || runtime.action != "devtools-open" {
		t.Fatalf("devtools action=%q runtime=%q err=%v", action, runtime.action, err)
	}
	for _, action := range []string{"devtools-toggle", "devtools-focus"} {
		if _, _, err := service.Execute(context.Background(), "s1", token, action, nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
			t.Fatalf("agent-facing %s error = %v", action, err)
		}
	}
	if _, _, err := service.Execute(context.Background(), "s1", token, "agent-browser-run", nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
		t.Fatalf("removed nested action error = %v", err)
	}
	if _, _, err := service.Execute(context.Background(), "s1", token, "eval", nil); apiErrorCode(err) != "BROWSER_ACTION_UNSUPPORTED" {
		t.Fatalf("unsupported action error = %v", err)
	}

	terminated := New(
		fakeSessions{session: domain.Session{SessionRecord: domain.SessionRecord{ID: "s1", IsTerminated: true}}},
		runtime,
		authority,
	)
	if _, err := terminated.Status(context.Background(), "s1", token); apiErrorCode(err) != "SESSION_TERMINATED" {
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
	if _, _, err := service.Execute(context.Background(), "s1", oldToken, "snapshot", nil); apiErrorCode(err) != "BROWSER_CAPABILITY_INVALID" {
		t.Fatalf("old bearer error = %v, want BROWSER_CAPABILITY_INVALID", err)
	}
	if _, _, err := service.Execute(context.Background(), "s1", newToken, "snapshot", nil); err != nil {
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
