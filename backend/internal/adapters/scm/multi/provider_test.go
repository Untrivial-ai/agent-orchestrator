package multi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeProvider struct {
	key            string
	parseOK        bool
	credsAvailable bool
	credsErr       error
	fetchCallCount int
	fetchErr       error
	// partialResults, when non-nil, overrides the default FetchPullRequests
	// behavior. The provider returns these observations (as-is) plus fetchErr.
	// This simulates a partial batch: some Fetched=true, some Fetched=false.
	partialResults []ports.SCMObservation
	identity       ports.SCMIdentity
	identityErr    error
}

func (f *fakeProvider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	if !f.parseOK {
		return ports.SCMRepo{}, false
	}
	return ports.SCMRepo{Provider: f.key, Host: f.key + ".com", Owner: "owner", Name: "repo", Repo: "owner/repo"}, true
}

func (f *fakeProvider) RepoPRListGuard(_ context.Context, _ ports.SCMRepo, etag string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{ETag: "etag-" + f.key, NotModified: etag == "etag-"+f.key}, nil
}

func (f *fakeProvider) ListPRsByRepo(_ context.Context, _ ports.SCMRepo, _ time.Time) ([]ports.SCMPRObservation, error) {
	return []ports.SCMPRObservation{{Number: 1, State: "open"}}, nil
}

func (f *fakeProvider) CommitChecksGuard(_ context.Context, _ ports.SCMRepo, _, etag string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{}, nil
}

func (f *fakeProvider) FetchPullRequests(_ context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	f.fetchCallCount++
	if f.partialResults != nil {
		return f.partialResults, f.fetchErr
	}
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	obs := make([]ports.SCMObservation, len(refs))
	for i, r := range refs {
		obs[i] = ports.SCMObservation{Fetched: true, Provider: f.key, PR: ports.SCMPRObservation{Number: r.Number}}
	}
	return obs, nil
}

func (f *fakeProvider) FetchFailedCheckLogTail(_ context.Context, _ ports.SCMRepo, _ ports.SCMCheckObservation) (string, error) {
	return "log tail from " + f.key, nil
}

func (f *fakeProvider) FetchReviewThreads(_ context.Context, _ ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return ports.SCMReviewObservation{Decision: "none"}, nil
}

func (f *fakeProvider) SCMCredentialsAvailable(_ context.Context) (bool, error) {
	return f.credsAvailable, f.credsErr
}

func (f *fakeProvider) AuthenticatedIdentity(_ context.Context) (ports.SCMIdentity, error) {
	return f.identity, f.identityErr
}

func TestParseRepository_RoutesToFirstMatch(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: false}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	repo, ok := m.ParseRepository("git@github.com:owner/repo.git")
	if !ok {
		t.Fatal("expected match")
	}
	if repo.Provider != "github" {
		t.Errorf("Provider = %q, want %q", repo.Provider, "github")
	}
}

func TestParseRepository_FallsThrough(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: false}
	gl := &fakeProvider{key: "gitlab", parseOK: true}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	repo, ok := m.ParseRepository("git@gitlab.com:owner/repo.git")
	if !ok {
		t.Fatal("expected match")
	}
	if repo.Provider != "gitlab" {
		t.Errorf("Provider = %q, want %q", repo.Provider, "gitlab")
	}
}

func TestParseRepository_NoMatch(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: false}
	gl := &fakeProvider{key: "gitlab", parseOK: false}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	_, ok := m.ParseRepository("git@unknown.com:owner/repo.git")
	if ok {
		t.Fatal("expected no match")
	}
}

func TestRouting_RepoPRListGuard(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: true}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	res, err := m.RepoPRListGuard(context.Background(), ports.SCMRepo{Provider: "gitlab"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.ETag != "etag-gitlab" {
		t.Errorf("ETag = %q, want %q (routed to wrong provider)", res.ETag, "etag-gitlab")
	}
}

func TestRouting_UnknownProvider(t *testing.T) {
	m := New(NamedProvider{Key: "github", Provider: &fakeProvider{key: "github"}})

	_, err := m.RepoPRListGuard(context.Background(), ports.SCMRepo{Provider: "gitlab"}, "")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestFetchPullRequests_PartitionsAndMerges(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: true}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 30},
	}

	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("got %d observations, want 3", len(obs))
	}
	if obs[0].Provider != "github" || obs[0].PR.Number != 10 {
		t.Errorf("obs[0] = %+v, want github/10", obs[0])
	}
	if obs[1].Provider != "gitlab" || obs[1].PR.Number != 20 {
		t.Errorf("obs[1] = %+v, want gitlab/20", obs[1])
	}
	if obs[2].Provider != "github" || obs[2].PR.Number != 30 {
		t.Errorf("obs[2] = %+v, want github/30", obs[2])
	}
	if gh.fetchCallCount != 1 {
		t.Errorf("github.FetchPullRequests called %d times, want 1", gh.fetchCallCount)
	}
	if gl.fetchCallCount != 1 {
		t.Errorf("gitlab.FetchPullRequests called %d times, want 1", gl.fetchCallCount)
	}
}

func TestSCMCredentialsAvailable_AnyTrue(t *testing.T) {
	gh := &fakeProvider{key: "github", credsAvailable: false}
	gl := &fakeProvider{key: "gitlab", credsAvailable: true}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	avail, err := m.SCMCredentialsAvailable(context.Background())
	if err != nil || !avail {
		t.Errorf("want (true, nil), got (%v, %v)", avail, err)
	}
}

func TestSCMCredentialsAvailable_NoneTrue(t *testing.T) {
	gh := &fakeProvider{key: "github", credsAvailable: false}
	gl := &fakeProvider{key: "gitlab", credsAvailable: false}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	avail, err := m.SCMCredentialsAvailable(context.Background())
	if err != nil || avail {
		t.Errorf("want (false, nil), got (%v, %v)", avail, err)
	}
}

// TestFetchPullRequests_PropagatesError verifies that when the only provider
// fails, no top-level error is returned. Instead, the failed observation carries
// the provider's error in obs.Error for per-observation routing (review finding #5
// / Ticket 02). Previously, the all-fail path returned an arbitrary map-iteration
// error, causing the observer to apply one error classification to all refs.
func TestFetchPullRequests_PropagatesError(t *testing.T) {
	fetchErr := errors.New("github down")
	gh := &fakeProvider{key: "github", fetchErr: fetchErr}
	m := New(NamedProvider{Key: "github", Provider: gh})

	refs := []ports.SCMPRRef{{Repo: ports.SCMRepo{Provider: "github"}, Number: 1}}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (all-fail path returns per-observation errors, not top-level)", err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1", len(obs))
	}
	if obs[0].Fetched {
		t.Errorf("obs[0].Fetched = true, want false for failed provider")
	}
	if !errors.Is(obs[0].Error, fetchErr) {
		t.Errorf("obs[0].Error = %v, want github failure %v", obs[0].Error, fetchErr)
	}
}

// TestFetchPullRequests_OneProviderFailsOthersSucceed verifies that when one
// provider fails, the other provider's successful observations are still
// returned and no error suppresses them. A GitLab timeout
// must not discard successful GitHub observations.
func TestFetchPullRequests_OneProviderFailsOthersSucceed(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: errors.New("gitlab timeout")}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (one provider failure must not suppress the other)", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	// GitHub observation must be present and correct despite GitLab failure.
	if !obs[0].Fetched || obs[0].Provider != "github" || obs[0].PR.Number != 10 {
		t.Errorf("obs[0] = %+v, want github/10 Fetched=true", obs[0])
	}
	// GitLab slot: the failing provider should leave a Fetched=false placeholder
	// so the observer can reject it without advancing durable state.
	if obs[1].Fetched {
		t.Errorf("obs[1].Fetched = true, want false for failed gitlab fetch")
	}
	// Item 7: the failed-provider observation must carry the error as transient
	// per-observation metadata so the observer can route it to cooldown or
	// refresh-incomplete handling.
	if obs[1].Error == nil {
		t.Errorf("obs[1].Error = nil, want the gitlab failure error for observer routing")
	}
	if gh.fetchCallCount != 1 {
		t.Errorf("github.FetchPullRequests called %d times, want 1", gh.fetchCallCount)
	}
	if gl.fetchCallCount != 1 {
		t.Errorf("gitlab.FetchPullRequests called %d times, want 1", gl.fetchCallCount)
	}
}

// TestFetchPullRequests_FailedProviderErrorCarriedAsMetadata verifies that a
// failed provider's error is attached to every failed-provider observation's
// Error field (Item 7). The observer relies on this field to route rate-limit
// errors to per-provider cooldown.
func TestFetchPullRequests_FailedProviderErrorCarriedAsMetadata(t *testing.T) {
	fetchErr := errors.New("gitlab 503")
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: fetchErr}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 21},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (one healthy provider)", err)
	}
	// GitHub obs: healthy, no error metadata.
	if obs[0].Error != nil {
		t.Errorf("obs[0].Error = %v, want nil for healthy github observation", obs[0].Error)
	}
	// Both GitLab obs must carry the same failure error.
	if !errors.Is(obs[1].Error, fetchErr) {
		t.Errorf("obs[1].Error = %v, want gitlab failure %v", obs[1].Error, fetchErr)
	}
	if !errors.Is(obs[2].Error, fetchErr) {
		t.Errorf("obs[2].Error = %v, want gitlab failure %v", obs[2].Error, fetchErr)
	}
	if obs[1].Fetched || obs[2].Fetched {
		t.Errorf("failed-provider observations must be Fetched=false")
	}
	if obs[1].Provider != "gitlab" || obs[2].Provider != "gitlab" {
		t.Errorf("failed-provider observations must keep their provider key for cooldown routing")
	}
}

// TestSCMCredentialsAvailable_SurfacesFirstRealError (Item 8) verifies that when
// no provider reports usable credentials, the first real error is returned
// (not nil) so CheckCredentialsOnce retries on the next poll rather than
// definitively disabling SCM observation.
func TestSCMCredentialsAvailable_SurfacesFirstRealError(t *testing.T) {
	credErr := errors.New("github probe 503")
	gh := &fakeProvider{key: "github", credsAvailable: false, credsErr: credErr}
	gl := &fakeProvider{key: "gitlab", credsAvailable: false, credsErr: nil}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	avail, err := m.SCMCredentialsAvailable(context.Background())
	if avail {
		t.Errorf("want available=false when no provider has credentials")
	}
	if err == nil {
		t.Fatal("want the first real credential error, got nil (must not be discarded)")
	}
	if !errors.Is(err, credErr) {
		t.Errorf("err = %v, want the first provider's real error %v", err, credErr)
	}
}

// TestSCMCredentialsAvailable_FirstErrorWinsWhenMultipleProvidersFail (Item 8)
// verifies that when MULTIPLE providers report transient credential errors, the
// FIRST provider's error (in registration order) is returned — not the second's,
// and not nil. This closes the gap left by SurfacesFirstRealError, which only has
// one provider with an error: the "first" in "first real error" is only truly
// tested when a second error exists to lose to the first. Against the pre-fix
// code (which discarded errors via `_ = err` and returned `(false, nil)`), this
// test fails at the `err == nil` assertion.
func TestSCMCredentialsAvailable_FirstErrorWinsWhenMultipleProvidersFail(t *testing.T) {
	ghErr := errors.New("github probe 503")
	glErr := errors.New("gitlab probe 502")
	gh := &fakeProvider{key: "github", credsAvailable: false, credsErr: ghErr}
	gl := &fakeProvider{key: "gitlab", credsAvailable: false, credsErr: glErr}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	avail, err := m.SCMCredentialsAvailable(context.Background())
	if avail {
		t.Errorf("want available=false when no provider has credentials")
	}
	if err == nil {
		t.Fatal("want the first real credential error, got nil (must not be discarded)")
	}
	if !errors.Is(err, ghErr) {
		t.Errorf("err = %v, want the first provider's error %v (github is registered first)", err, ghErr)
	}
	if errors.Is(err, glErr) {
		t.Errorf("err = %v, must not be the second provider's error %v", err, glErr)
	}
}

// TestSCMCredentialsAvailable_HealthyProviderSuppressesError verifies that a
// healthy provider's success still wins even when another provider returned a
// transient credential error (the composite must report available=true, nil).
func TestSCMCredentialsAvailable_HealthyProviderSuppressesError(t *testing.T) {
	gh := &fakeProvider{key: "github", credsAvailable: false, credsErr: errors.New("github probe 503")}
	gl := &fakeProvider{key: "gitlab", credsAvailable: true, credsErr: nil}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	avail, err := m.SCMCredentialsAvailable(context.Background())
	if err != nil || !avail {
		t.Errorf("want (true, nil) when one provider is healthy, got (%v, %v)", avail, err)
	}
}

// TestFetchPullRequests_AllProvidersFail verifies that when ALL providers
// fail, no top-level error is returned (Ticket 02). Each failed observation
// carries its provider's error in obs.Error for per-observation routing.
// Previously, the all-fail path returned an arbitrary map-iteration error,
// causing the observer to apply one error classification to ALL refs across
// ALL providers — e.g. treating a GitLab auth error as a GitHub rate-limit.
func TestFetchPullRequests_AllProvidersFail(t *testing.T) {
	ghErr := errors.New("github down")
	glErr := errors.New("gitlab down")
	gh := &fakeProvider{key: "github", parseOK: true, fetchErr: ghErr}
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: glErr}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (all-fail path returns per-observation errors, not top-level)", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	// Each observation must carry its own provider's error, not an arbitrary one.
	if !errors.Is(obs[0].Error, ghErr) {
		t.Errorf("obs[0].Error = %v, want github failure %v", obs[0].Error, ghErr)
	}
	if !errors.Is(obs[1].Error, glErr) {
		t.Errorf("obs[1].Error = %v, want gitlab failure %v", obs[1].Error, glErr)
	}
	if obs[0].Fetched || obs[1].Fetched {
		t.Errorf("all-fail observations must be Fetched=false")
	}
	// Ensure the errors are NOT cross-contaminated.
	if errors.Is(obs[0].Error, glErr) {
		t.Errorf("obs[0].Error = gitlab failure; must carry github's own error, not gitlab's")
	}
	if errors.Is(obs[1].Error, ghErr) {
		t.Errorf("obs[1].Error = github failure; must carry gitlab's own error, not github's")
	}
}

// TestFetchPullRequests_PartialBatchSingleProvider verifies that when a single
// sub-provider returns partial results (some Fetched=true, some Fetched=false)
// plus a non-nil error, the multi provider returns a nil top-level error (review
// finding #5). The observer's chunk loop sees err == nil and processes each
// observation individually — the Fetched=true one is persisted, the Fetched=false
// one is routed via its .Error field. Without this fix, the single-group case
// hits len(groupErrs) == len(groups) (1 == 1) and returns a top-level error,
// causing the observer to discard ALL results including the Fetched=true one.
func TestFetchPullRequests_PartialBatchSingleProvider(t *testing.T) {
	fetchErr := errors.New("gitlab partial failure")
	gl := &fakeProvider{
		key:      "gitlab",
		parseOK:  true,
		fetchErr: fetchErr,
		partialResults: []ports.SCMObservation{
			{Fetched: true, Provider: "gitlab", PR: ports.SCMPRObservation{Number: 10}},
			{Fetched: false, Provider: "gitlab", PR: ports.SCMPRObservation{Number: 20}},
		},
	}
	m := New(NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (partial batch with Fetched=true must not return top-level error)", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	// The successful observation must be present.
	if !obs[0].Fetched || obs[0].Provider != "gitlab" || obs[0].PR.Number != 10 {
		t.Errorf("obs[0] = %+v, want gitlab/10 Fetched=true", obs[0])
	}
	if obs[0].Error != nil {
		t.Errorf("obs[0].Error = %v, want nil for successful observation", obs[0].Error)
	}
	// The failed observation must be present with Fetched=false and carry the error.
	if obs[1].Fetched {
		t.Errorf("obs[1].Fetched = true, want false for failed ref")
	}
	if !errors.Is(obs[1].Error, fetchErr) {
		t.Errorf("obs[1].Error = %v, want gitlab failure %v", obs[1].Error, fetchErr)
	}
}

// TestFetchPullRequests_SingleProviderAllFail verifies that when a single
// sub-provider returns all Fetched=false observations plus a non-nil error,
// no top-level error is returned (Ticket 02). Each failed observation carries
// the provider's error in obs.Error for per-observation routing. The observer's
// per-observation routing at the !obs.Fetched branch classifies rate-limit
// vs non-rate-limit per observation, and the "missing from chunk" loop calls
// markRepoRefreshFailed for all Fetched=false observations.
func TestFetchPullRequests_SingleProviderAllFail(t *testing.T) {
	fetchErr := errors.New("gitlab down")
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: fetchErr}
	m := New(NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (all-fail path returns per-observation errors, not top-level)", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	if obs[0].Fetched || obs[1].Fetched {
		t.Errorf("all-fail observations must be Fetched=false")
	}
	if !errors.Is(obs[0].Error, fetchErr) {
		t.Errorf("obs[0].Error = %v, want gitlab failure %v", obs[0].Error, fetchErr)
	}
	if !errors.Is(obs[1].Error, fetchErr) {
		t.Errorf("obs[1].Error = %v, want gitlab failure %v", obs[1].Error, fetchErr)
	}
}

// TestFetchPullRequests_TwoProvidersOneFailsOneSucceeds verifies that when two
// providers are registered and one fully fails while the other fully succeeds,
// the multi provider returns a nil top-level error and the healthy provider's
// observations are present (review finding #5).
func TestFetchPullRequests_TwoProvidersOneFailsOneSucceeds(t *testing.T) {
	gh := &fakeProvider{key: "github", parseOK: true}
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: errors.New("gitlab down")}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "github"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab"}, Number: 20},
	}
	obs, err := m.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("error = %v, want nil (one healthy provider)", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	// GitHub observation must be present and correct despite GitLab failure.
	if !obs[0].Fetched || obs[0].Provider != "github" || obs[0].PR.Number != 10 {
		t.Errorf("obs[0] = %+v, want github/10 Fetched=true", obs[0])
	}
	// GitLab slot: Fetched=false placeholder with the error attached.
	if obs[1].Fetched {
		t.Errorf("obs[1].Fetched = true, want false for failed gitlab fetch")
	}
}

// TestAuthenticatedIdentityForProvider_DelegatesToCorrectSubProvider verifies
// that AuthenticatedIdentityForProvider resolves the identity from the
// sub-provider matching the given key (finding #7).
func TestAuthenticatedIdentityForProvider_DelegatesToCorrectSubProvider(t *testing.T) {
	ghIdentity := ports.SCMIdentity{Login: "octocat", Human: true}
	glIdentity := ports.SCMIdentity{Login: "gitlab-bot", Human: false}
	gh := &fakeProvider{key: "github", identity: ghIdentity}
	gl := &fakeProvider{key: "gitlab", identity: glIdentity}
	m := New(NamedProvider{Key: "github", Provider: gh}, NamedProvider{Key: "gitlab", Provider: gl})

	got, err := m.AuthenticatedIdentityForProvider(context.Background(), "github", "")
	if err != nil {
		t.Fatalf("github: unexpected error: %v", err)
	}
	if got != ghIdentity {
		t.Errorf("github identity = %+v, want %+v", got, ghIdentity)
	}

	got, err = m.AuthenticatedIdentityForProvider(context.Background(), "gitlab", "")
	if err != nil {
		t.Fatalf("gitlab: unexpected error: %v", err)
	}
	if got != glIdentity {
		t.Errorf("gitlab identity = %+v, want %+v", got, glIdentity)
	}
}

// TestAuthenticatedIdentityForProvider_UnknownProviderReturnsError verifies
// that requesting identity for an unregistered provider key returns an error
// (finding #7).
func TestAuthenticatedIdentityForProvider_UnknownProviderReturnsError(t *testing.T) {
	gh := &fakeProvider{key: "github", identity: ports.SCMIdentity{Login: "octocat"}}
	m := New(NamedProvider{Key: "github", Provider: gh})

	_, err := m.AuthenticatedIdentityForProvider(context.Background(), "bitbucket", "")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestFetchPullRequests_FailedPlaceholderCarriesHostAndRepo verifies that
// Fetched=false placeholders created by the multi provider carry Host and
// Repo from the ref's SCMRepo. Without these fields, the observer's
// prKeyFromObs returns an empty key, causing the placeholder to be skipped
// at the `if key == "" { continue }` guard — the per-observation error
// routing (rate-limit cooldown vs refresh-incomplete) is never reached and
// the failed provider is retried every tick instead of entering cooldown.
// This test covers both placeholder sites: the unknown-provider branch and
// the provider-returned-fewer-than-refs branch.
func TestFetchPullRequests_FailedPlaceholderCarriesHostAndRepo(t *testing.T) {
	// Case 1: unknown provider — the first placeholder construction site.
	// When the only provider is unknown, the all-fail path returns a top-level
	// error, but the placeholder is still constructed with Host/Repo.
	m := New(NamedProvider{Key: "github", Provider: &fakeProvider{key: "github"}})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "owner", Name: "repo", Repo: "owner/repo"}, Number: 42},
	}
	obs, _ := m.FetchPullRequests(context.Background(), refs)
	if obs[0].Fetched {
		t.Errorf("obs[0].Fetched = true, want false for unknown provider")
	}
	if obs[0].Host != "gitlab.com" {
		t.Errorf("obs[0].Host = %q, want %q", obs[0].Host, "gitlab.com")
	}
	if obs[0].Repo != "owner/repo" {
		t.Errorf("obs[0].Repo = %q, want %q", obs[0].Repo, "owner/repo")
	}

	// Case 2: provider returns fewer observations than refs (the second site).
	// Simulate by returning partial results: one obs for two refs.
	gl := &fakeProvider{
		key:      "gitlab",
		parseOK:  true,
		fetchErr: errors.New("gitlab 503"),
		partialResults: []ports.SCMObservation{
			{Fetched: true, Provider: "gitlab", PR: ports.SCMPRObservation{Number: 10}},
		},
	}
	m2 := New(NamedProvider{Key: "gitlab", Provider: gl})

	refs2 := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "group", Name: "proj", Repo: "group/proj"}, Number: 10},
		{Repo: ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "group", Name: "proj", Repo: "group/proj"}, Number: 20},
	}
	obs2, _ := m2.FetchPullRequests(context.Background(), refs2)
	if obs2[1].Fetched {
		t.Errorf("obs2[1].Fetched = true, want false for missing obs")
	}
	if obs2[1].Host != "gitlab.com" {
		t.Errorf("obs2[1].Host = %q, want %q", obs2[1].Host, "gitlab.com")
	}
	if obs2[1].Repo != "group/proj" {
		t.Errorf("obs2[1].Repo = %q, want %q", obs2[1].Repo, "group/proj")
	}

	// Case 3: Repo field empty — fallback to Owner + "/" + Name.
	refs3 := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "unknown", Host: "self-hosted.example", Owner: "myorg", Name: "myrepo"}, Number: 5},
	}
	m3 := New(NamedProvider{Key: "github", Provider: &fakeProvider{key: "github"}})
	obs3, _ := m3.FetchPullRequests(context.Background(), refs3)
	if obs3[0].Repo != "myorg/myrepo" {
		t.Errorf("obs3[0].Repo = %q, want %q (fallback to Owner/Name)", obs3[0].Repo, "myorg/myrepo")
	}
	if obs3[0].Host != "self-hosted.example" {
		t.Errorf("obs3[0].Host = %q, want %q", obs3[0].Host, "self-hosted.example")
	}
}

// TestFetchPullRequests_FailedPlaceholderProducesNonEmptyKey verifies that
// the observer's prKeyFromObs returns a non-empty key for a failed-provider
// placeholder. This is the integration assertion: without Host/Repo on the
// placeholder, prKeyFromObs returns "" and the observer skips it at the
// `if key == "" { continue }` guard, preventing per-observation error routing.
func TestFetchPullRequests_FailedPlaceholderProducesNonEmptyKey(t *testing.T) {
	gl := &fakeProvider{key: "gitlab", parseOK: true, fetchErr: errors.New("gitlab rate limit")}
	m := New(NamedProvider{Key: "gitlab", Provider: gl})

	refs := []ports.SCMPRRef{
		{Repo: ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "owner", Name: "repo", Repo: "owner/repo"}, Number: 99},
	}
	obs, _ := m.FetchPullRequests(context.Background(), refs)

	// The observer's prKeyFromObs builds: Provider + ":" + Host + ":" + Repo + "#" + Number
	// All three components must be non-empty for the key to be non-empty.
	key := obs[0].Provider + ":" + obs[0].Host + ":" + obs[0].Repo + "#" + fmt.Sprint(obs[0].PR.Number)
	if key == "" || key == "gitlab::#99" {
		t.Errorf("prKey would be empty or incomplete: %q (Host=%q Repo=%q)", key, obs[0].Host, obs[0].Repo)
	}
	wantKey := "gitlab:gitlab.com:owner/repo#99"
	if key != wantKey {
		t.Errorf("prKey = %q, want %q", key, wantKey)
	}
}

// fakeHostScopedProvider wraps fakeProvider and adds per-host identity
// resolution, simulating a GitLab provider with self-managed hosts.
type fakeHostScopedProvider struct {
	*fakeProvider
	hostIdentities map[string]ports.SCMIdentity
	hostErrs       map[string]error
	hostCalls      []string
}

func (f *fakeHostScopedProvider) AuthenticatedIdentityForHost(_ context.Context, host string) (ports.SCMIdentity, error) {
	f.hostCalls = append(f.hostCalls, host)
	if err, ok := f.hostErrs[host]; ok {
		return ports.SCMIdentity{}, err
	}
	if id, ok := f.hostIdentities[host]; ok {
		return id, nil
	}
	return ports.SCMIdentity{}, fmt.Errorf("no identity for host %q", host)
}

// TestAuthenticatedIdentityForProvider_DelegatesHostToSubProvider verifies
// that the multi provider passes the host parameter through to host-scoped
// sub-providers (GitLab), so a self-managed host gets the correct identity
// (ticket 05).
func TestAuthenticatedIdentityForProvider_DelegatesHostToSubProvider(t *testing.T) {
	ghIdentity := ports.SCMIdentity{Login: "octocat", Human: true}
	glDefault := ports.SCMIdentity{Login: "gl-dot-com-user", Human: true}
	glSelf := ports.SCMIdentity{Login: "self-managed-user", Human: true}

	gh := &fakeProvider{key: "github", identity: ghIdentity}
	gl := &fakeHostScopedProvider{
		fakeProvider: &fakeProvider{key: "gitlab", identity: glDefault},
		hostIdentities: map[string]ports.SCMIdentity{
			"":                glDefault,
			"gitlab.internal": glSelf,
		},
	}
	m := New(
		NamedProvider{Key: "github", Provider: gh},
		NamedProvider{Key: "gitlab", Provider: gl},
	)

	// GitHub ignores host — delegates to AuthenticatedIdentity.
	got, err := m.AuthenticatedIdentityForProvider(context.Background(), "github", "github.com")
	if err != nil {
		t.Fatalf("github: unexpected error: %v", err)
	}
	if got != ghIdentity {
		t.Errorf("github identity = %+v, want %+v", got, ghIdentity)
	}
	if len(gl.hostCalls) != 0 {
		t.Errorf("github should not call host-scoped method; got calls %v", gl.hostCalls)
	}

	// GitLab gitlab.com (empty host) — delegates to AuthenticatedIdentityForHost("").
	got, err = m.AuthenticatedIdentityForProvider(context.Background(), "gitlab", "")
	if err != nil {
		t.Fatalf("gitlab default: unexpected error: %v", err)
	}
	if got != glDefault {
		t.Errorf("gitlab default identity = %+v, want %+v", got, glDefault)
	}

	// GitLab self-managed host — delegates to AuthenticatedIdentityForHost("gitlab.internal").
	got, err = m.AuthenticatedIdentityForProvider(context.Background(), "gitlab", "gitlab.internal")
	if err != nil {
		t.Fatalf("gitlab self-managed: unexpected error: %v", err)
	}
	if got != glSelf {
		t.Errorf("gitlab self-managed identity = %+v, want %+v", got, glSelf)
	}

	// Verify the host parameter was passed through correctly.
	if len(gl.hostCalls) != 2 || gl.hostCalls[0] != "" || gl.hostCalls[1] != "gitlab.internal" {
		t.Errorf("host calls = %v, want [\"\" \"gitlab.internal\"]", gl.hostCalls)
	}
}
