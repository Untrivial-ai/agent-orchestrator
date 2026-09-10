package lifecycle

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSCMObservationEquivalentCIAttemptsAfterRestart(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = working("mer-1")
	obs := ports.SCMObservation{
		Fetched: true,
		PR:      ports.SCMPRObservation{URL: "https://github.com/o/r/pull/1", HeadSHA: "sha1"},
		CI: ports.SCMCIObservation{Summary: string(domain.CIFailing), HeadSHA: "sha1", FailedChecks: []ports.SCMCheckObservation{
			{Name: "build", Status: string(domain.PRCheckFailed), URL: "run/1", ProviderID: "1", LogTail: "2026-09-01T12:00:00.001Z compile error"},
			{Name: "test", Status: string(domain.PRCheckFailed), URL: "run/2", ProviderID: "2", LogTail: "assertion failed"},
		}},
	}
	first := &fakeMessenger{}
	if err := New(st, first).ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(first.msgs) != 1 {
		t.Fatalf("first messages = %#v", first.msgs)
	}
	obs.CI.FailedChecks = []ports.SCMCheckObservation{
		{Name: "test", Status: string(domain.PRCheckFailed), URL: "run/4", ProviderID: "4", LogTail: "assertion failed"},
		{Name: "build", Status: string(domain.PRCheckFailed), URL: "run/3", ProviderID: "3", LogTail: "2026-09-01T12:05:00.123Z compile error"},
	}
	next := &fakeMessenger{}
	m := New(st, next)
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(next.msgs) != 0 {
		t.Fatalf("equivalent retry notified again: %#v", next.msgs)
	}
	obs.CI.FailedChecks[0].LogTail = "different assertion failed"
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(next.msgs) != 1 {
		t.Fatalf("changed failure messages = %#v", next.msgs)
	}
	obs.CI.HeadSHA = "sha2"
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(next.msgs) != 2 {
		t.Fatalf("new commit messages = %#v", next.msgs)
	}
}

func TestSCMObservationBillingUnknownDoesNotNudge(t *testing.T) {
	m, st, msg := newManager()
	st.sessions["mer-1"] = working("mer-1")
	obs := ports.SCMObservation{Fetched: true, PR: ports.SCMPRObservation{URL: "pr1"}, CI: ports.SCMCIObservation{
		Summary: string(domain.CIUnknown), Checks: []ports.SCMCheckObservation{{Name: "build", Status: string(domain.PRCheckUnknown), Conclusion: "failure", LogTail: "Job blocked by billing"}},
	}}
	if err := m.ApplySCMObservation(ctx, "mer-1", obs); err != nil {
		t.Fatal(err)
	}
	if len(msg.msgs) != 0 {
		t.Fatalf("billing refusal sent repair instruction: %#v", msg.msgs)
	}
}

func TestCIFailureSignatureNormalizesOnlyAttemptMetadata(t *testing.T) {
	original := ports.PRCheckObservation{Name: "build", CommitHash: "sha", Status: domain.PRCheckFailed, URL: "run/1", LogTail: "bad timestamp 2026-09-01T12:00:00Z"}
	if ciFailureSignature([]ports.PRCheckObservation{original}) != ciFailureSignature([]ports.PRCheckObservation{original, original}) {
		t.Fatal("repeated check entries changed failure identity")
	}
	for _, change := range []string{"name", "commit", "content", "embedded timestamp"} {
		t.Run(change, func(t *testing.T) {
			next := original
			switch change {
			case "name":
				next.Name = "test"
			case "commit":
				next.CommitHash = "sha2"
			case "content":
				next.LogTail = "different failure"
			case "embedded timestamp":
				next.LogTail = "bad timestamp 2026-09-01T12:00:01Z"
			}
			if ciFailureSignature([]ports.PRCheckObservation{original}) == ciFailureSignature([]ports.PRCheckObservation{next}) {
				t.Fatal("meaningful change did not update failure identity")
			}
		})
	}
}
