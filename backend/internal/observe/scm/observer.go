// Package scm implements the provider-neutral SCM polling observer. It owns the
// polling loop, ETag/cache checks, semantic diffing, DB persistence, and
// lifecycle notification; provider adapters only normalize provider-specific
// APIs into ports.SCMObservation values.
package scm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	// DefaultTickInterval is the SCM observer's PR/CI polling cadence.
	DefaultTickInterval = 30 * time.Second
	// DefaultReviewInterval is the minimum interval between review-thread polls
	// for a PR whose review state warrants thread refresh.
	DefaultReviewInterval = 2 * time.Minute
	// DefaultCacheMax bounds each in-memory ETag/review cache map.
	DefaultCacheMax = 512
	// DefaultPRMaxAge bounds how long a locally-open tracked PR may go without a
	// forced re-fetch. It is a bounded-staleness backstop, not a distrust of the
	// ETag: the refresh signals the observer consults (the repo PR-list guard and
	// the per-commit checks guard) do not track a PR's own state, so a merged or
	// closed PR whose head SHA is unchanged can otherwise read stale. This caps
	// how long that inference can persist.
	DefaultPRMaxAge = 5 * time.Minute
	// BatchSize is the maximum number of PRs in one provider batch fetch.
	BatchSize = 25

	// fallbackIdentityKey is the map key used when the observer falls back
	// to the single-provider IdentityResolver path. It represents the
	// unnamed identity that applies when no ScopedIdentityResolver is wired.
	fallbackIdentityKey = ""
)

// identityKey builds the cache key for a per-provider, per-host identity.
// GitHub repos carry Host="github.com" but GitHub identity is not
// host-scoped, so all GitHub hosts collapse to the same key. GitLab repos
// on self-managed hosts get a distinct key from gitlab.com.
func identityKey(provider, host string) string {
	if provider == "github" {
		return provider // GitHub identity is not host-scoped
	}
	return provider + "|" + host
}

// Provider is the normalized SCM provider contract used by the observer.
type Provider interface {
	ParseRepository(remote string) (ports.SCMRepo, bool)
	RepoPRListGuard(ctx context.Context, repo ports.SCMRepo, etag string) (ports.SCMGuardResult, error)
	ListPRsByRepo(ctx context.Context, repo ports.SCMRepo, updatedAfter time.Time) ([]ports.SCMPRObservation, error)
	CommitChecksGuard(ctx context.Context, repo ports.SCMRepo, headSHA, etag string) (ports.SCMGuardResult, error)
	FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error)
	FetchFailedCheckLogTail(ctx context.Context, repo ports.SCMRepo, check ports.SCMCheckObservation) (string, error)
	FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error)
}

// Store is the persistence contract the observer needs for discovery, local
// hash reads, and transactional SCM writes.
type Store interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
	UpsertProject(ctx context.Context, row domain.ProjectRecord) error
	ListWorkspaceRepos(ctx context.Context, projectID string) ([]domain.WorkspaceRepoRecord, error)
	ListPRsBySession(ctx context.Context, sessionID domain.SessionID) ([]domain.PullRequest, error)
	ListChecks(ctx context.Context, prURL string) ([]domain.PullRequestCheck, error)
	WriteSCMObservation(ctx context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, reviewMode ports.ReviewWriteMode) error
}

// Lifecycle is the provider-neutral lifecycle notification sink.
type Lifecycle interface {
	ApplySCMObservation(ctx context.Context, sessionID domain.SessionID, obs ports.SCMObservation) error
}

type credentialChecker interface {
	SCMCredentialsAvailable(ctx context.Context) (bool, error)
}

// rateLimitedError is the optional capability of a provider error that carries
// structured retry hints (Retry-After / RateLimit-Reset). The gitlab and
// github adapters' RateLimitError types both satisfy this via errors.As; a
// bare sentinel without structured hints falls through to the bounded default.
type rateLimitedError interface {
	GetRetryAfter() time.Duration
	GetResetAt() time.Time
}

// rateLimitCooldown extracts the cooldown duration from a provider rate-limit
// error. It prefers the provider's Retry-After, falls back to ResetAt, and
// finally to a bounded default with jitter so the observer does not hammer
// the API every 30s while rate-limited. Returns ok=false
// when err is not a rate-limit error.
func rateLimitCooldown(now time.Time, err error) (time.Duration, bool) {
	var rl rateLimitedError
	if !errors.As(err, &rl) {
		return 0, false
	}
	if ra := rl.GetRetryAfter(); ra > 0 {
		return boundedCooldown(ra), true
	}
	if reset := rl.GetResetAt(); !reset.IsZero() && reset.After(now) {
		return boundedCooldown(reset.Sub(now)), true
	}
	return defaultRateLimitCooldown(now), true
}

// boundedCooldown clamps the provider-suggested cooldown to a sane window so a
// misbehaving server cannot force the observer to sleep for hours.
const (
	minRateLimitCooldown = 30 * time.Second
	maxRateLimitCooldown = 10 * time.Minute
	// incrementalDiscoveryOverlap is subtracted from the last sync cursor so
	// MRs updated near the cursor boundary are not missed due to clock skew
	// or updated_at granularity
	incrementalDiscoveryOverlap = 5 * time.Minute
)

func boundedCooldown(d time.Duration) time.Duration {
	if d < minRateLimitCooldown {
		d = minRateLimitCooldown
	}
	if d > maxRateLimitCooldown {
		d = maxRateLimitCooldown
	}
	return d
}

func defaultRateLimitCooldown(now time.Time) time.Duration {
	// 60s ± 15s jitter, deterministic per clock tick so tests using a fixed
	// clock are reproducible.
	base := 60 * time.Second
	jitter := time.Duration(now.UnixNano()%int64(15*time.Second)) - 7*time.Second
	return boundedCooldown(base + jitter)
}

// Config holds optional observer knobs. Zero values use production defaults.
type Config struct {
	// Tick is the fast PR/CI polling interval. Zero uses DefaultTickInterval.
	Tick time.Duration
	// ReviewInterval is the slower review-thread refresh interval.
	ReviewInterval time.Duration
	// Clock supplies timestamps for observations and tests. Nil uses time.Now.
	Clock func() time.Time
	// Logger receives operational diagnostics for provider/store/lifecycle failures.
	Logger *slog.Logger
	// CacheMax bounds each in-memory ETag/review cache. Zero uses DefaultCacheMax.
	CacheMax int
	// IdentityResolver resolves the active SCM account lazily. Nil preserves branch-based discovery.
	IdentityResolver ports.SCMIdentityResolver
	// ScopedIdentityResolver resolves the authenticated identity per provider key.
	// When set, the observer resolves identities for all providers upfront in
	// each poll and checks PR authors against the matching provider's identity.
	// When nil, the observer falls back to IdentityResolver (single-provider).
	ScopedIdentityResolver ports.ScopedIdentityResolver
}

// ObserverCache stores provider ETags and review polling timestamps in memory.
// It is intentionally non-persistent for v1; cold restarts simply revalidate.
type ObserverCache struct {
	// RepoPRListETag maps repository keys to the last open-PR-list ETag.
	RepoPRListETag map[string]string
	// CommitChecksETag maps repo+commit keys to the last check-runs ETag.
	CommitChecksETag map[string]string
	// LastReviewPollAt maps PR keys to the last review-thread fetch timestamp.
	LastReviewPollAt map[string]time.Time
	// ReviewRefreshFailed marks PRs whose review-thread refresh failed; the
	// next poll retries regardless of the normal review cadence/status rules.
	ReviewRefreshFailed map[string]bool
	// LastPRFetchAt maps PR keys to the last time the PR was successfully fetched from the provider.
	LastPRFetchAt map[string]time.Time
	// lastPRFetchOrder tracks FIFO eviction order for LastPRFetchAt.
	lastPRFetchOrder []string
	// LastSyncCursor maps repository keys to the last successful incremental
	// PR-list sync timestamp. ListPRsByRepo is called with updated_after =
	// cursor - overlap; the cursor advances only after successful persistence
	//
	LastSyncCursor map[string]time.Time
	// repoOrder tracks FIFO eviction order for RepoPRListETag.
	repoOrder []string
	// commitOrder tracks FIFO eviction order for CommitChecksETag.
	commitOrder []string
	// lastReviewPollOrder tracks FIFO eviction order for LastReviewPollAt.
	lastReviewPollOrder []string
	// reviewFailedOrder tracks FIFO eviction order for ReviewRefreshFailed.
	reviewFailedOrder []string
	// max is the maximum number of entries each cache map retains.
	max int
}

func newCache(maxEntries int) ObserverCache {
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMax
	}
	return ObserverCache{
		RepoPRListETag:      map[string]string{},
		CommitChecksETag:    map[string]string{},
		LastReviewPollAt:    map[string]time.Time{},
		ReviewRefreshFailed: map[string]bool{},
		LastPRFetchAt:       map[string]time.Time{},
		LastSyncCursor:      map[string]time.Time{},
		max:                 maxEntries,
	}
}

// Observer coordinates provider polling, semantic diffing, persistence, and
// lifecycle notifications for SCM observations.
type Observer struct {
	// provider is the SCM adapter used for all provider/network operations.
	provider Provider
	// store supplies sessions/projects/local PR state and receives transactional writes.
	store Store
	// lifecycle is notified after successful persistence of meaningful changes.
	lifecycle Lifecycle
	// tick is the active PR/CI polling cadence.
	tick time.Duration
	// reviewInterval is the minimum duration between review-thread fetches per PR.
	reviewInterval time.Duration
	// clock supplies observation timestamps.
	clock func() time.Time
	// logger receives non-fatal operational failures.
	logger *slog.Logger
	// credentialsChecked records whether an optional provider credential gate ran.
	credentialsChecked bool
	// disabled is set after the credential gate reports unavailable credentials.
	disabled bool
	// identityResolver is the explicitly wired source of the active SCM account.
	identityResolver ports.SCMIdentityResolver
	// scopedIdentityResolver resolves the authenticated identity per provider key.
	scopedIdentityResolver ports.ScopedIdentityResolver
	// rateLimitUntil records, per provider key, the time until which that
	// provider's calls should be skipped. Set when a provider returns a
	// rate-limit error so the observer applies a cooldown instead of polling
	// every 30s
	rateLimitUntil map[string]time.Time
	// Cache holds bounded in-memory provider ETags and review poll timestamps.
	Cache ObserverCache
}

// New constructs an Observer with default cadence/cache settings for zero
// values in cfg.
func New(provider Provider, store Store, lifecycle Lifecycle, cfg Config) *Observer {
	o := &Observer{provider: provider, store: store, lifecycle: lifecycle, tick: cfg.Tick, reviewInterval: cfg.ReviewInterval, clock: cfg.Clock, logger: cfg.Logger, identityResolver: cfg.IdentityResolver, scopedIdentityResolver: cfg.ScopedIdentityResolver, Cache: newCache(cfg.CacheMax), rateLimitUntil: map[string]time.Time{}}
	if o.tick <= 0 {
		o.tick = DefaultTickInterval
	}
	if o.reviewInterval <= 0 {
		o.reviewInterval = DefaultReviewInterval
	}
	if o.clock == nil {
		o.clock = time.Now
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// Start launches the observer loop. The first Poll runs immediately inside the
// goroutine so daemon startup is not blocked; subsequent polls run on the tick.
//
// The first invocation of poll inside the supervisor also runs checkCredentials
// up front. That way the "scm observer disabled: provider credentials
// unavailable" warning is emitted on a fresh daemon even if discoverSubjects
// has no subjects yet (which would otherwise short-circuit Poll before
// checkCredentials). checkCredentials is guarded by credentialsChecked, so the
// wrap stays once-per-process; a transient error there simply defers the check
// to the next tick.
func (o *Observer) Start(ctx context.Context) <-chan struct{} {
	var credentialGate sync.Once
	poll := func(ctx context.Context) error {
		credentialGate.Do(func() {
			if _, err := o.checkCredentials(ctx); err != nil && !errors.Is(err, context.Canceled) {
				o.logger.Error("scm observer: initial credential check failed", "err", err)
			}
		})
		return o.Poll(ctx)
	}
	return observe.StartPollLoop(ctx, o.tick, poll, o.logger, "scm observer")
}

type subject struct {
	session domain.SessionRecord
	repo    ports.SCMRepo
	branch  string
	known   domain.PullRequest
	hasPR   bool
}

// sessionRepo pairs a live session with a repo to scan and its branch for
// per-repo branch-prefix discovery of new (including stacked) pull requests.
// A session is scanned against its push origin plus every other remote in the
// project checkout, so repo is the repo whose open-PR list is listed while
// headRepo is the repo the session's head branch actually lives in (the push
// origin). For same-repo PRs repo == headRepo; for a cross-fork PR (fork head,
// upstream base) repo is the upstream base and headRepo is the fork origin.
type sessionRepo struct {
	session  domain.SessionRecord
	repo     ports.SCMRepo
	headRepo ports.SCMRepo
	branch   string
}

type repoGuardState struct {
	result  ports.SCMGuardResult
	hadETag bool
	err     error
}

type pendingCacheString struct {
	key   string
	value string
}

type refreshSelection struct {
	refs          []ports.SCMPRRef
	subjectsByPR  map[string]*subject
	commitETags   map[string]pendingCacheString
	candidateKeys map[string]bool
}

type persistenceOptions struct {
	reviewFetched               bool
	preserveLocalMetadataHash   bool
	preserveLocalCIHash         bool
	preserveLocalReviewHash     bool
	preserveLocalReviewDecision bool
}

// Poll runs one synchronous SCM observation cycle.
func (o *Observer) Poll(ctx context.Context) error {
	now := o.clock().UTC()
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.disabled {
		return nil
	}
	subjects, sessionRepos, err := o.discoverSubjects(ctx)
	if err != nil {
		return err
	}
	if len(sessionRepos) == 0 {
		return nil
	}
	proceed, err := o.checkCredentials(ctx)
	if err != nil {
		return err
	}
	if !proceed || o.disabled {
		return nil
	}

	repoGuards := o.guardRepos(ctx, sessionRepos)
	repoRefreshOK := pendingRepoRefreshes(repoGuards)
	// repoListFailed records repos whose PR listing (ListPRsByRepo) failed
	// during this poll. markRepoRefreshOK checks this set and refuses to
	// flip a repo back to OK when the listing itself failed — without this,
	// a successful PR write for a different PR in the same repo would clear
	// the listing failure and advance the ETag/cursor, making the failed
	// listing unrecoverable on the next poll.
	repoListFailed := map[string]bool{}
	markRepoRefreshFailed := func(repo ports.SCMRepo) {
		key := prKey(repo, 0)
		if _, ok := repoRefreshOK[key]; ok {
			repoRefreshOK[key] = false
		}
	}
	// markRepoListFailed is called only when the PR listing itself fails
	// (ListPRsByRepo error in discoverNewPRs). It sets repoListFailed so
	// markRepoRefreshOK cannot clear it within the same poll, and also
	// marks the repo refresh-incomplete via markRepoRefreshFailed.
	markRepoListFailed := func(repo ports.SCMRepo) {
		repoListFailed[prKey(repo, 0)] = true
		markRepoRefreshFailed(repo)
	}
	// markRepoRefreshOK un-marks a repo as refresh-incomplete. It is used by
	// the terminal-reconciliation path: a reconciled PR that turns out to be
	// a terminal transition (merged/closed) is persisted normally, and the
	// repo ETag/cursor may advance. A "still open" no-op result does NOT
	// call this, so the ETag/cursor stay pinned (cross-cutting durable-state
	// preservation rule).
	//
	// Monotonicity guard: if the repo's listing failed this poll
	// (repoListFailed), do NOT flip it back to OK. A successful PR write for
	// a different PR must not clear a listing-level failure.
	markRepoRefreshOK := func(repo ports.SCMRepo) {
		key := prKey(repo, 0)
		if repoListFailed[key] {
			return
		}
		if g, ok := repoGuards[key]; ok && g.err == nil && g.result.ETag != "" {
			repoRefreshOK[key] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listedPRs, listedRepos := o.discoverNewPRs(ctx, sessionRepos, subjects, repoGuards, now, markRepoListFailed)
	if err := ctx.Err(); err != nil {
		return err
	}

	selection := o.selectRefreshCandidates(ctx, subjects, repoGuards, listedPRs, markRepoRefreshFailed, now)
	// Item 2 — terminal-state reconciliation for GitHub: tracked open PRs
	// not in the current state=open listing may have transitioned to
	// merged/closed. Run a reconciliation pass that issues a full detail
	// fetch per reconciled PR so terminal transitions are not permanently
	// missed. The pass runs only when the repo-list guard is not a 304,
	// and only for GitHub (asymmetry — see reconcileTerminalGitHubPRs).
	// Terminal observations are routed through the normal persistence path;
	// "still open" results are no-op persists that mark the repo
	// refresh-incomplete so the ETag/cursor do not advance.
	reconciledObs := o.reconcileTerminalGitHubPRs(ctx, subjects, repoGuards, listedPRs, &selection, now, markRepoRefreshFailed)
	observations := map[string]ports.SCMObservation{}
	for key, obs := range reconciledObs {
		observations[key] = obs
	}
	prRefreshOK := map[string]bool{}
	for key := range selection.candidateKeys {
		prRefreshOK[key] = false
	}
	for _, chunk := range chunks(selection.refs, BatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Skip the entire chunk if every ref's provider is under a rate-limit
		// cooldown; otherwise filter out the cooled-down providers so a
		// rate-limited GitLab does not suppress healthy GitHub refs in the
		// same chunk
		active := chunk[:0]
		for _, ref := range chunk {
			if o.inRateLimitCooldown(now, ref.Repo.Provider) {
				// Item 3 — cooldown-skip marks refresh-incomplete: when a ref
				// is skipped under cooldown, its repository is marked as
				// refresh-incomplete so the repo ETag and LastSyncCursor do
				// not advance without an observation being fetched. Without
				// this, a subsequent 304 can make the skipped update
				// unrecoverable (cross-cutting durable-state preservation
				// rule, review finding #1).
				markRepoRefreshFailed(ref.Repo)
				continue
			}
			active = append(active, ref)
		}
		if len(active) == 0 {
			continue
		}
		batch, err := o.provider.FetchPullRequests(ctx, active)
		if err != nil {
			// If the failure is a rate-limit error, apply a per-provider
			// cooldown so subsequent polls back off instead of hammering
			// every 30s
			if cooldown, ok := rateLimitCooldown(now, err); ok {
				for _, ref := range active {
					o.setRateLimitCooldown(now, ref.Repo.Provider, cooldown)
				}
				o.logger.Warn("scm observer: provider rate-limited; entering cooldown", "cooldown", cooldown, "err", err)
				for _, ref := range active {
					markRepoRefreshFailed(ref.Repo)
				}
				continue
			}
			o.logger.Error("scm observer: GraphQL PR batch failed", "err", err)
			for _, ref := range active {
				markRepoRefreshFailed(ref.Repo)
			}
			continue
		}
		chunkSeen := map[string]bool{}
		for _, obs := range batch {
			obs.ObservedAt = now
			key := prKeyFromObs(obs)
			if key == "" {
				continue
			}
			// Reject Fetched=false observations from transient failures so
			// they do not overwrite durable metadata/CI/review facts. The
			// provider returns Fetched=false + a non-nil error; the
			// placeholder must not advance ETags or be persisted (review
			// finding #1).
			//
			// Per-observation Error routing (review Item 7): when the
			// multi dispatcher attaches a failure as transient metadata on
			// a Fetched=false observation (one provider failed while
			// another succeeded), route it here so the failed provider is
			// not retried every tick:
			//   - rate-limit error → per-provider cooldown (reuses existing
			//     rateLimitCooldown/setRateLimitCooldown machinery);
			//   - non-rate-limit error → mark the repo refresh-incomplete.
			if !obs.Fetched {
				if obs.Error != nil {
					providerKey := obs.Provider
					if providerKey == "" {
						// Fall back to the ref's provider when the placeholder
						// did not carry one (defensive — multi always sets it).
						for _, ref := range active {
							if prKey(ref.Repo, ref.Number) == key {
								providerKey = ref.Repo.Provider
								break
							}
						}
					}
					if cooldown, ok := rateLimitCooldown(now, obs.Error); ok {
						if providerKey != "" {
							o.setRateLimitCooldown(now, providerKey, cooldown)
						}
						o.logger.Warn("scm observer: provider rate-limited (per-observation); entering cooldown", "provider", providerKey, "cooldown", cooldown, "err", obs.Error)
					} else {
						o.logger.Warn("scm observer: provider fetch failed (per-observation); marking refresh-incomplete", "provider", providerKey, "err", obs.Error)
					}
				}
				continue
			}
			observations[key] = obs
			chunkSeen[key] = true
		}
		for _, ref := range active {
			key := prKey(ref.Repo, ref.Number)
			if !chunkSeen[key] {
				markRepoRefreshFailed(ref.Repo)
			}
		}
	}

	for key, subj := range selection.subjectsByPR {
		if err := ctx.Err(); err != nil {
			return err
		}
		obs, ok := observations[key]
		if !ok {
			continue
		}
		local := subj.known
		o.enrichFailureLogs(ctx, &obs, local)
		observations[key] = obs
	}

	reviewModes := map[string]ports.ReviewWriteMode{}
	localOnlyObservations := map[string]bool{}
	reviewStale := map[string]bool{}
	o.refreshReviews(ctx, subjects, observations, selection.subjectsByPR, reviewModes, localOnlyObservations, reviewStale, now)
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, key := range dispatchOrder(observations, selection.subjectsByPR) {
		if err := ctx.Err(); err != nil {
			return err
		}
		obs := observations[key]
		// Nil out transient per-observation failure metadata before persistence.
		// obs.Error is routing-only metadata (review Item 7): it carries a
		// provider failure for cooldown/refresh-incomplete routing above, and
		// must never reach the storage layer or lifecycle so durable state is
		// not corrupted by transient failures (cross-cutting rule, finding 1).
		obs.Error = nil
		observations[key] = obs
		subj, ok := selection.subjectsByPR[key]
		if !ok {
			continue
		}
		local := subj.known
		reviewMode := reviewModes[key]
		opts := persistenceOptions{
			reviewFetched:               reviewMode != ports.ReviewWritePreserve,
			preserveLocalMetadataHash:   localOnlyObservations[key],
			preserveLocalCIHash:         localOnlyObservations[key] || obs.CI.Partial,
			preserveLocalReviewHash:     reviewStale[key],
			preserveLocalReviewDecision: reviewStale[key],
		}
		prepared := o.prepareForPersistence(obs, local, opts, now)
		if !prepared.Changed.Metadata && !prepared.Changed.CI && !prepared.Changed.Review {
			prRefreshOK[key] = true
			continue
		}
		finalPR, finalChecks, finalReviews, finalThreads, finalComments := domainFromObservation(subj.session.ID, subj.session, prepared, local, opts, now)
		pr, checks, reviews, threads, comments := finalPR, finalChecks, finalReviews, finalThreads, finalComments
		// Lifecycle is allowed to run only after the observed facts are durable,
		// but semantic hashes are the observer's acknowledgement cursor. Keep
		// changed hashes at their local values until lifecycle succeeds; if the
		// daemon restarts after a lifecycle failure, the stale hashes force the
		// same observation to be fetched and delivered again.
		if o.lifecycle != nil {
			pendingOpts := opts
			if prepared.Changed.Metadata {
				pendingOpts.preserveLocalMetadataHash = true
			}
			if prepared.Changed.CI {
				pendingOpts.preserveLocalCIHash = true
			}
			if prepared.Changed.Review {
				pendingOpts.preserveLocalReviewHash = true
			}
			pr, checks, reviews, threads, comments = domainFromObservation(subj.session.ID, subj.session, prepared, local, pendingOpts, now)
		}
		if err := o.store.WriteSCMObservation(ctx, pr, checks, reviews, threads, comments, reviewMode); err != nil {
			o.logger.Error("scm observer: DB write failed", "session", subj.session.ID, "pr", pr.URL, "err", err)
			markRepoRefreshFailed(subj.repo)
			continue
		}
		if o.lifecycle != nil {
			if err := o.lifecycle.ApplySCMObservation(ctx, subj.session.ID, prepared); err != nil {
				o.logger.Error("scm observer: lifecycle notification failed", "session", subj.session.ID, "pr", firstNonEmpty(prepared.PR.URL, prepared.PR.HTMLURL, local.URL), "err", err)
				markRepoRefreshFailed(subj.repo)
				continue
			}
			if err := o.store.WriteSCMObservation(ctx, finalPR, finalChecks, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
				o.logger.Error("scm observer: DB lifecycle acknowledgement failed", "session", subj.session.ID, "pr", finalPR.URL, "err", err)
				markRepoRefreshFailed(subj.repo)
				continue
			}
		}
		// If this observation came from terminal reconciliation, a successful
		// terminal persistence (merged/closed transition observed and
		// persisted) un-marks the repo refresh-incomplete so the repo
		// ETag/cursor may advance. A "still open" no-op result never reaches
		// this branch (it returns at the unchanged-hashes check above), so
		// its repo stays refresh-incomplete and the ETag/cursor stay pinned.
		markRepoRefreshOK(subj.repo)
		prRefreshOK[key] = true
	}
	for key, ok := range prRefreshOK {
		if !ok {
			continue
		}
		if pending, found := selection.commitETags[key]; found {
			o.cacheSetString(o.Cache.CommitChecksETag, &o.Cache.commitOrder, pending.key, pending.value)
		}
		if reviewModes[key] != ports.ReviewWritePreserve {
			o.cacheSetTime(o.Cache.LastReviewPollAt, &o.Cache.lastReviewPollOrder, key, now)
		}
		o.cacheSetTime(o.Cache.LastPRFetchAt, &o.Cache.lastPRFetchOrder, key, now)
	}
	// Per-ref monotonicity (finding #1): build a map of repoKey → candidate
	// PR keys so the final cursor-advance loop can verify that every ref in
	// a repo succeeded before advancing the repo-level ETag/cursor. Without
	// this, a single failed ref followed by a successful observation for a
	// different PR in the same repo restores repoRefreshOK=true and advances
	// the cursor, making the failed update unrecoverable.
	repoCandidateKeys := map[string][]string{}
	for pk := range selection.candidateKeys {
		subj, ok := selection.subjectsByPR[pk]
		if !ok {
			continue
		}
		rk := prKey(subj.repo, 0)
		repoCandidateKeys[rk] = append(repoCandidateKeys[rk], pk)
	}
	for key, ok := range repoRefreshOK {
		if !ok {
			continue
		}
		// The repo ETag/cursor advances only when every candidate ref in
		// that repo has prRefreshOK == true. A single failed ref prevents
		// the cursor from advancing so the failed update is recoverable on
		// the next poll.
		allRefsOK := true
		for _, pk := range repoCandidateKeys[key] {
			if !prRefreshOK[pk] {
				allRefsOK = false
				break
			}
		}
		if !allRefsOK {
			continue
		}
		if etag := repoGuards[key].result.ETag; etag != "" {
			o.cacheSetString(o.Cache.RepoPRListETag, &o.Cache.repoOrder, key, etag)
		}
		// Advance the incremental-discovery cursor only after the repo's PR
		// list was fetched AND all its observations were successfully
		// persisted, so a transient failure does not skip MRs updated during
		// the failed poll
		if listedRepos[key] {
			o.Cache.LastSyncCursor[key] = now
		}
	}
	return nil
}

// dispatchOrder returns observation keys in a deterministic order so lifecycle
// notifications for a session are stable across polls.
func dispatchOrder(observations map[string]ports.SCMObservation, subjectsByPR map[string]*subject) []string {
	keys := make([]string, 0, len(observations))
	for key := range observations {
		keys = append(keys, key)
	}
	sessionOf := func(key string) string {
		if s := subjectsByPR[key]; s != nil {
			return string(s.session.ID)
		}
		return ""
	}
	sort.Slice(keys, func(i, j int) bool {
		if si, sj := sessionOf(keys[i]), sessionOf(keys[j]); si != sj {
			return si < sj
		}
		if ni, nj := observations[keys[i]].PR.Number, observations[keys[j]].PR.Number; ni != nj {
			return ni < nj
		}
		return keys[i] < keys[j]
	})
	return keys
}
func (o *Observer) checkCredentials(ctx context.Context) (bool, error) {
	var probe observe.CredentialProbe
	if checker, ok := o.provider.(credentialChecker); ok {
		probe = checker.SCMCredentialsAvailable
	}
	return observe.CheckCredentialsOnce(ctx, probe, &o.credentialsChecked, &o.disabled, o.logger, "scm observer")
}

// discoverSubjects builds the per-PR refresh subjects (normally one per open
// tracked PR)
// and the per-session repo list used for branch-prefix discovery of new PRs. A
// session may own several PRs, so each open tracked PR becomes its own subject.
// Terminal PRs stay eligible only for an opted-in live session, allowing an
// unacknowledged teardown failure to be delivered again on a later poll.
func (o *Observer) discoverSubjects(ctx context.Context) (map[string]*subject, []sessionRepo, error) {
	sessions, err := o.store.ListAllSessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	projects := map[domain.ProjectID]domain.ProjectRecord{}
	originRepos := map[domain.ProjectID]ports.SCMRepo{}
	scanRepos := map[domain.ProjectID][]ports.SCMRepo{}
	out := map[string]*subject{}
	var sessionRepos []sessionRepo
	for _, sess := range sessions {
		if sess.IsTerminated {
			continue
		}
		branch := strings.TrimSpace(sess.Metadata.Branch)
		if branch == "" {
			continue
		}
		proj, ok := projects[sess.ProjectID]
		if !ok {
			p, found, err := o.store.GetProject(ctx, string(sess.ProjectID))
			if err != nil {
				return nil, nil, err
			}
			if !found || !p.ArchivedAt.IsZero() {
				continue
			}
			if p.RepoOriginURL == "" && p.Path != "" {
				if url := resolveGitOriginURL(p.Path); url != "" {
					p.RepoOriginURL = url
					if err := o.store.UpsertProject(ctx, p); err != nil {
						o.logger.Warn("scm observer: backfill origin URL persist failed", "project", p.ID, "err", err)
					}
				}
			}
			projects[sess.ProjectID] = p
			proj = p
			if origin, ok := o.provider.ParseRepository(p.RepoOriginURL); ok {
				originRepos[sess.ProjectID] = origin
				scanRepos[sess.ProjectID] = o.resolveScanRepos(p, origin)
			}
		}
		repos := make([]ports.SCMRepo, 0, len(scanRepos[sess.ProjectID]))
		if origin, ok := originRepos[sess.ProjectID]; ok {
			for _, repo := range scanRepos[sess.ProjectID] {
				sessionRepos = append(sessionRepos, sessionRepo{session: sess, repo: repo, headRepo: origin, branch: branch})
				repos = append(repos, repo)
			}
		}
		childRepos, err := o.workspaceSCMSessionRepos(ctx, proj, sess, branch)
		if err != nil {
			return nil, nil, err
		}
		for _, child := range childRepos {
			sessionRepos = append(sessionRepos, child)
			repos = append(repos, child.repo)
		}
		if len(repos) == 0 {
			o.logger.Debug("scm observer: project has no supported SCM origins", "project", proj.ID)
			continue
		}
		prs, err := o.store.ListPRsBySession(ctx, sess.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, pr := range trackedPRsForSession(sess, prs) {
			prRepo, ok := repoForTrackedPR(pr, repos)
			if !ok {
				o.logger.Warn("scm observer: tracked PR repo no longer belongs to project", "session", sess.ID, "pr", pr.URL, "repo", pr.Repo)
				continue
			}
			key := prKey(prRepo, pr.Number)
			if existing, ok := out[key]; ok {
				o.logger.Warn("scm observer: duplicate tracked PR ownership skipped", "pr", key, "kept_session", existing.session.ID, "skipped_session", sess.ID)
				continue
			}
			out[key] = &subject{session: sess, repo: prRepo, branch: branch, known: pr, hasPR: true}
		}
	}
	return out, sessionRepos, nil
}

// resolveScanRepos returns the deduped set of repos whose open-PR lists should be
// scanned to attribute PRs to this project's sessions: the push origin plus every
// other GitHub remote configured in the project checkout (upstreams, mirrors).
// Attribution still requires a PR's head branch to live in the origin, so scanning
// extra remotes only surfaces cross-fork PRs (fork head, upstream base) and can
// never misattribute a stranger's PR.
//
// ponytail: remotes are read once per project per process (memoized by the
// caller); a remote added after the daemon started is picked up on restart. Move
// to a git-config watch if that latency ever matters.
func (o *Observer) resolveScanRepos(proj domain.ProjectRecord, origin ports.SCMRepo) []ports.SCMRepo {
	repos := []ports.SCMRepo{origin}
	if strings.TrimSpace(proj.Path) == "" {
		return repos
	}
	seen := map[string]bool{prKey(origin, 0): true}
	for _, url := range gitRemoteURLsFunc(proj.Path) {
		repo, ok := o.provider.ParseRepository(url)
		if !ok {
			continue
		}
		key := prKey(repo, 0)
		if seen[key] {
			continue
		}
		seen[key] = true
		repos = append(repos, repo)
	}
	return repos
}

func (o *Observer) workspaceSCMSessionRepos(ctx context.Context, proj domain.ProjectRecord, sess domain.SessionRecord, branch string) ([]sessionRepo, error) {
	if proj.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return nil, nil
	}
	childRepos, err := o.store.ListWorkspaceRepos(ctx, proj.ID)
	if err != nil {
		return nil, err
	}
	repos := make([]sessionRepo, 0, len(childRepos))
	seen := map[string]bool{}
	for _, child := range childRepos {
		if strings.TrimSpace(child.RepoOriginURL) == "" {
			continue
		}
		repo, ok := o.provider.ParseRepository(child.RepoOriginURL)
		if !ok {
			o.logger.Debug("scm observer: unsupported SCM origin", "project", proj.ID, "repo", child.Name, "origin", child.RepoOriginURL)
			continue
		}
		childPath := filepath.Join(proj.Path, filepath.FromSlash(child.RelativePath))
		for _, scanRepo := range o.resolveScanRepos(domain.ProjectRecord{Path: childPath}, repo) {
			key := prKey(scanRepo, 0)
			if seen[key] {
				continue
			}
			seen[key] = true
			repos = append(repos, sessionRepo{session: sess, repo: scanRepo, headRepo: repo, branch: branch})
		}
	}
	return repos, nil
}

func repoForTrackedPR(pr domain.PullRequest, repos []ports.SCMRepo) (ports.SCMRepo, bool) {
	if pr.Provider != "" && pr.Host != "" && pr.Repo != "" {
		owner, name, ok := strings.Cut(pr.Repo, "/")
		if !ok || owner == "" || name == "" {
			return ports.SCMRepo{}, false
		}
		return ports.SCMRepo{Provider: pr.Provider, Host: pr.Host, Owner: owner, Name: name, Repo: pr.Repo}, true
	}
	if pr.Repo != "" {
		for _, repo := range repos {
			if matchesTrackedPRRepo(pr, repo) {
				return repo, true
			}
		}
		return ports.SCMRepo{}, false
	}
	if len(repos) == 1 {
		return repos[0], true
	}
	for _, repo := range repos {
		if strings.EqualFold(repo.Repo, repos[0].Repo) {
			return repo, true
		}
	}
	return repos[0], len(repos) > 0
}

func matchesTrackedPRRepo(pr domain.PullRequest, repo ports.SCMRepo) bool {
	if pr.Provider != "" && !strings.EqualFold(pr.Provider, repo.Provider) {
		return false
	}
	if pr.Host != "" && !strings.EqualFold(pr.Host, repo.Host) {
		return false
	}
	if pr.Repo != "" && !strings.EqualFold(pr.Repo, repoFullName(repo)) {
		return false
	}
	return true
}

func openTrackedPRs(prs []domain.PullRequest) []domain.PullRequest {
	out := make([]domain.PullRequest, 0, len(prs))
	for _, pr := range prs {
		if pr.Number > 0 && !pr.Merged && !pr.Closed {
			out = append(out, pr)
		}
	}
	return out
}

// trackedPRsForSession keeps terminal PRs discoverable only while an opted-in
// session remains live. If merge-driven teardown fails, the observer has not
// acknowledged the terminal semantic hash, so a later poll can redeliver it.
func trackedPRsForSession(sess domain.SessionRecord, prs []domain.PullRequest) []domain.PullRequest {
	if !sess.TerminateOnPRMerge {
		return openTrackedPRs(prs)
	}
	out := make([]domain.PullRequest, 0, len(prs))
	for _, pr := range prs {
		if pr.Number > 0 {
			out = append(out, pr)
		}
	}
	return out
}

func (o *Observer) guardRepos(ctx context.Context, sessionRepos []sessionRepo) map[string]repoGuardState {
	repos := map[string]ports.SCMRepo{}
	for _, sr := range sessionRepos {
		repos[prKey(sr.repo, 0)] = sr.repo
	}
	out := map[string]repoGuardState{}
	for key, repo := range repos {
		prev, had := o.Cache.RepoPRListETag[key]
		res, err := o.provider.RepoPRListGuard(ctx, repo, prev)
		if err != nil {
			o.logger.Error("scm observer: repo PR-list guard failed", "repo", repoFullName(repo), "err", err)
			out[key] = repoGuardState{hadETag: had, err: err}
			continue
		}
		out[key] = repoGuardState{result: res, hadETag: had}
	}
	return out
}

func pendingRepoRefreshes(guards map[string]repoGuardState) map[string]bool {
	out := map[string]bool{}
	for key, g := range guards {
		if g.err == nil && g.result.ETag != "" {
			out[key] = true
		}
	}
	return out
}

// discoverNewPRs lists each repo's open PRs once and attaches any not-yet-tracked
// PR to the session that owns its source branch. A session owns a PR when the
// PR's source branch equals the session branch or descends from it (the
// "branch/..." stacking convention). One session may therefore pick up several
// PRs (its root plus stacked children). Repos whose PR-list guard reports
// NotModified against a known ETag are skipped, since nothing new can have
// appeared since the last poll.
func (o *Observer) discoverNewPRs(ctx context.Context, sessionRepos []sessionRepo, subjects map[string]*subject, guards map[string]repoGuardState, now time.Time, markRepoFailed func(ports.SCMRepo)) (listedPRs, listedRepos map[string]bool) {
	// Resolve identities per-provider when a ScopedIdentityResolver is wired.
	// This ensures GitHub PRs are checked against the GitHub identity and
	// GitLab PRs against the GitLab identity. Falls back to the single-
	// provider IdentityResolver path for backward compatibility.
	identities, identityKnown := o.resolveIdentities(ctx, sessionRepos)
	byRepo := map[string][]sessionRepo{}
	repos := map[string]ports.SCMRepo{}
	for _, sr := range sessionRepos {
		key := prKey(sr.repo, 0)
		byRepo[key] = append(byRepo[key], sr)
		repos[key] = sr.repo
	}
	// listed tracks which repos had their PR list fetched this poll, and
	// listedPRs tracks which specific PRs were in the updated set. These drive
	// incremental refresh candidate selection so only updated MRs are
	// re-fetched
	listedPRs = map[string]bool{}
	listedRepos = map[string]bool{}
	for repoKey, repo := range repos {
		g := guards[repoKey]
		if g.err != nil {
			continue
		}
		if g.result.NotModified && g.hadETag {
			continue
		}
		// Incremental discovery: request only MRs updated since the last
		// successful cursor minus an overlap window. Zero cursor (first poll)
		// requests a full listing
		repoKey := prKey(repo, 0)
		cursor := o.Cache.LastSyncCursor[repoKey]
		updatedAfter := time.Time{}
		if !cursor.IsZero() {
			updatedAfter = cursor.Add(-incrementalDiscoveryOverlap)
		}
		pulls, err := o.provider.ListPRsByRepo(ctx, repo, updatedAfter)
		if err != nil {
			o.logger.Debug("scm observer: open PR list failed", "repo", repoFullName(repo), "err", err)
			if markRepoFailed != nil && !errors.Is(err, ports.ErrSCMNotFound) {
				markRepoFailed(repo)
			}
			continue
		}
		// Record the listed PR numbers so selectRefreshCandidates can
		// restrict refresh to only updated MRs rather than every tracked MR.
		listedRepos[repoKey] = true
		for _, pr := range pulls {
			listedPRs[prKey(repo, pr.Number)] = true
		}
		for _, pr := range pulls {
			if pr.Number <= 0 || pr.SourceBranch == "" {
				continue
			}
			if identityKnown {
				id, ok := identities[identityKey(repo.Provider, repo.Host)]
				if !ok {
					id, ok = identities[fallbackIdentityKey] // fallback single-identity
				}
				if ok && !strings.EqualFold(strings.TrimSpace(pr.Author), id.Login) {
					continue
				}
			}
			key := prKey(repo, pr.Number)
			if _, ok := subjects[key]; ok {
				continue
			}
			// Branch-prefix attribution must only claim PRs whose head branch
			// lives in a session's push origin. A same-repo PR has head == origin
			// == this scanned repo; a cross-fork PR (fork head, upstream base) has
			// head == origin while this scanned repo is the upstream base. A
			// stranger's fork PR carries a head repo no session owns and is
			// dropped (as is an empty head repo from a deleted fork), preserving
			// the no-misattribution guarantee.
			eligible := candidatesForHeadRepo(byRepo[repoKey], pr.HeadRepo)
			sr, ok := matchSession(eligible, pr.SourceBranch)
			if !ok {
				continue
			}
			known := domain.PullRequest{
				URL:          firstNonEmpty(pr.URL, pr.HTMLURL),
				SessionID:    sr.session.ID,
				Number:       pr.Number,
				Draft:        pr.Draft,
				SourceBranch: pr.SourceBranch,
				TargetBranch: pr.TargetBranch,
				HeadSHA:      pr.HeadSHA,
				Provider:     repo.Provider,
				Host:         repo.Host,
				Repo:         repoFullName(repo),
				ProviderID:   pr.ProviderID,
				UpdatedAt:    now,
			}
			// Persist the discovered PR as an open baseline row immediately, before
			// the refresh/lifecycle pass runs. A session can own several PRs, and a
			// terminal observation for one of them triggers a completion check that
			// reads every PR of the session from the store. Without this write, an
			// open sibling/child discovered in the same poll would not yet be
			// durable, and the session could terminate while that PR is still open.
			if err := o.store.WriteSCMObservation(ctx, known, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
				o.logger.Error("scm observer: persist discovered PR failed", "session", sr.session.ID, "pr", known.URL, "err", err)
				if markRepoFailed != nil {
					markRepoFailed(repo)
				}
				continue
			}
			subjects[key] = &subject{
				session: sr.session,
				repo:    repo,
				branch:  sr.branch,
				known:   known,
				hasPR:   true,
			}
		}
	}
	return listedPRs, listedRepos
}

func (o *Observer) authenticatedIdentity(ctx context.Context) (ports.SCMIdentity, bool) {
	if o.identityResolver == nil {
		return ports.SCMIdentity{}, false
	}
	identity, err := o.identityResolver.AuthenticatedIdentity(ctx)
	if err != nil {
		o.logger.Debug("scm observer: authenticated identity unavailable; preserving branch-based discovery", "err", err)
		return ports.SCMIdentity{}, false
	}
	identity.Login = strings.TrimSpace(identity.Login)
	if !identity.Human || identity.Login == "" {
		o.logger.Debug("scm observer: authenticated human identity unavailable; preserving branch-based discovery")
		return ports.SCMIdentity{}, false
	}
	return identity, true
}

// resolveIdentities resolves the authenticated identity for each provider key
// present in sessionRepos. When a ScopedIdentityResolver is wired, identities
// are resolved upfront (one call per unique provider+host pair) and cached in
// a map keyed by identityKey(provider, host). This ensures a self-managed
// GitLab host gets its own identity, distinct from gitlab.com. If identity
// resolution fails for one provider+host, PRs from that provider+host fall
// back to branch-based discovery while other providers continue normally.
// When no ScopedIdentityResolver is available, it falls back to the
// single-provider IdentityResolver path.
func (o *Observer) resolveIdentities(ctx context.Context, sessionRepos []sessionRepo) (map[string]ports.SCMIdentity, bool) {
	if o.scopedIdentityResolver != nil {
		seen := map[string]bool{}
		identities := make(map[string]ports.SCMIdentity)
		anyKnown := false
		for _, sr := range sessionRepos {
			ik := identityKey(sr.repo.Provider, sr.repo.Host)
			if seen[ik] {
				continue
			}
			seen[ik] = true
			id, err := o.scopedIdentityResolver.AuthenticatedIdentityForProvider(ctx, sr.repo.Provider, sr.repo.Host)
			if err != nil {
				o.logger.Debug("scm observer: per-provider identity unavailable; preserving branch-based discovery for provider", "provider", sr.repo.Provider, "host", sr.repo.Host, "err", err)
				continue
			}
			id.Login = strings.TrimSpace(id.Login)
			if !id.Human || id.Login == "" {
				o.logger.Debug("scm observer: per-provider human identity unavailable; preserving branch-based discovery for provider", "provider", sr.repo.Provider, "host", sr.repo.Host)
				continue
			}
			identities[ik] = id
			anyKnown = true
		}
		return identities, anyKnown
	}
	// Fallback: single-provider IdentityResolver path.
	identity, ok := o.authenticatedIdentity(ctx)
	if !ok {
		return nil, false
	}
	return map[string]ports.SCMIdentity{fallbackIdentityKey: identity}, true
}

// matchSession picks the session that owns sourceBranch. A session owns the
// branch when it is an exact match or a stacked descendant ("branch/..."). The
// default worker branch is a leaf named "<namespace>/root"; for that shape the
// session also owns sibling branches under "<namespace>/..." so Git can create
// child PR branches without colliding with the root ref. When several session
// branches are prefixes of the same source branch the longest (most specific)
// one wins, so a child session claims its own stacked PRs rather than the
// ancestor session.
// candidatesForHeadRepo narrows the scanned repo's session candidates to those
// whose head branch lives in headRepo (the PR's head repository full name). This
// is the fork guard: a PR is only attributable when its head repo equals a
// session's push origin, whether the PR was found on the origin itself or on a
// scanned upstream base repo.
func candidatesForHeadRepo(candidates []sessionRepo, headRepo string) []sessionRepo {
	if strings.TrimSpace(headRepo) == "" {
		return nil
	}
	var out []sessionRepo
	for _, sr := range candidates {
		if strings.EqualFold(repoFullName(sr.headRepo), headRepo) {
			out = append(out, sr)
		}
	}
	return out
}

func matchSession(candidates []sessionRepo, sourceBranch string) (sessionRepo, bool) {
	for _, sr := range candidates {
		if sr.branch != "" && sr.branch == sourceBranch {
			return sr, true
		}
	}
	var best sessionRepo
	bestLen := -1
	for _, sr := range candidates {
		if sr.branch == "" {
			continue
		}
		for _, prefix := range sessionBranchPrefixes(sr.branch) {
			if prefix == sourceBranch || strings.HasPrefix(sourceBranch, prefix+"/") {
				if len(prefix) > bestLen {
					best = sr
					bestLen = len(prefix)
				}
			}
		}
	}
	return best, bestLen >= 0
}

func sessionBranchPrefixes(branch string) []string {
	prefixes := []string{branch}
	if namespace, ok := strings.CutSuffix(branch, "/root"); ok && namespace != "" {
		prefixes = append(prefixes, namespace)
	}
	return prefixes
}

func (o *Observer) selectRefreshCandidates(ctx context.Context, subjects map[string]*subject, guards map[string]repoGuardState, listedPRs map[string]bool, markRepoFailed func(ports.SCMRepo), now time.Time) refreshSelection {
	selection := refreshSelection{
		subjectsByPR:  map[string]*subject{},
		commitETags:   map[string]pendingCacheString{},
		candidateKeys: map[string]bool{},
	}
	for _, s := range subjects {
		if !s.hasPR || s.known.Number <= 0 {
			continue
		}
		key := prKey(s.repo, s.known.Number)
		selection.subjectsByPR[key] = s
		candidate := missingLocalState(s.known)
		repoCursor := o.Cache.LastSyncCursor[prKey(s.repo, 0)]
		hasCursor := !repoCursor.IsZero()
		g := guards[prKey(s.repo, 0)]
		if g.err == nil && !g.result.NotModified {
			// Incremental discovery: on polls after the first (hasCursor), only
			// refresh MRs that appeared in the updated set. On the first poll
			// (no cursor), refresh all tracked MRs
			if hasCursor && listedPRs != nil {
				if listedPRs[key] {
					candidate = true
				}
			} else {
				candidate = true
			}
		}
		if s.known.HeadSHA != "" {
			commitKey := commitKey(s.repo, s.known.HeadSHA)
			prev := o.Cache.CommitChecksETag[commitKey]
			res, err := o.provider.CommitChecksGuard(ctx, s.repo, s.known.HeadSHA, prev)
			if err != nil {
				o.logger.Error("scm observer: commit check-runs guard failed", "pr", s.known.URL, "sha", s.known.HeadSHA, "err", err)
				if markRepoFailed != nil {
					markRepoFailed(s.repo)
				}
			} else if !res.NotModified && res.ETag != "" && res.ETag != prev {
				// Item 1 — commit-check ETag independent refresh: a changed
				// commit-check ETag promotes this PR to refresh candidate
				// regardless of whether the PR appears in the current
				// repository listing. The previous `listedPRs[key]` gate
				// meant that once a sync cursor existed and the repo-list
				// guard returned 304 (leaving listedPRs empty), CI state
				// changes (pending→passing/failing) were silently dropped.
				// The decoupling ensures CI transitions observed via the
				// commit-check ETag are always persisted. The ETag must
				// actually change (non-empty and different from the cached
				// value) so an empty guard result does not spuriously
				// promote every tracked PR on every poll.
				candidate = true
				selection.commitETags[key] = pendingCacheString{key: commitKey, value: res.ETag}
			}
		}
		if !candidate {
			// Force an unconditional fetch once the snapshot is older than
			// DefaultPRMaxAge. Trade-off: GitHub does not bill 304 responses, so
			// this forgoes the conditional-request savings the ETag path bought.
			// At the current 30s tick / 5m max-age that is ~12 unconditional
			// fetches/hour per tracked open PR, and it scales linearly with the
			// number of tracked PRs. It also fires for every stale PR in the same
			// tick, so watch secondary-rate-limit burstiness as fleets grow.
			//
			// When incremental discovery is active (hasCursor), PRs not in the
			// updated set are left to the terminal-reconciliation pass — do not
			// promote them here or reconciliation will skip them (it checks
			// candidateKeys to avoid double-fetching).
			if !hasCursor || listedPRs == nil || listedPRs[key] {
				last := o.Cache.LastPRFetchAt[key]
				if last.IsZero() || now.Sub(last) > DefaultPRMaxAge {
					candidate = true
				}
			}
		}
		if candidate {
			selection.refs = append(selection.refs, ports.SCMPRRef{Repo: s.repo, Number: s.known.Number, URL: s.known.URL})
			selection.candidateKeys[key] = true
		}
	}
	return selection
}

// reconcileTerminalGitHubPRs (Item 2) runs a terminal-state reconciliation
// pass for tracked open GitHub PRs that are not in the current state=open
// listing. Such PRs may have transitioned to merged/closed, which GitHub's
// state=open listing permanently drops before their terminal state can be
// observed by the normal refresh path. The pass issues a full detail fetch per
// reconciled PR and routes the result through the normal observation/persistence
// machinery.
//
// The pass runs on every poll where the GitHub repo-list guard is NOT a 304
// (i.e. the listing changed). A "still open" detail result is a no-op
// persistence: the observation is returned but the repository is marked
// refresh-incomplete so the repo ETag and sync cursor do not advance (cross-
// cutting durable-state preservation rule). A terminal (merged/closed) result
// is persisted normally so the terminal transition is observed.
//
// The pass is unbounded — no per-poll cap on reconciliation fetches. The worst
// case (one PR updated, 49 others reconciled) is bounded by the tracked-open-
// PR count, which is small in AO's use case (sessions track the PRs they
// spawned).
//
// ASYMMETRY — this pass is GitHub-only by deliberate design:
//   - GitHub uses state=open on ListPRsByRepo and RepoPRListGuard, so terminal
//     PRs disappear from the listing before their detail can be refreshed.
//     The reviewer asked for tracked-PR terminal reconciliation on GitHub
//     (review Item 2), which state=open requires.
//   - GitLab uses state=all on ListPRsByRepo (approved from the first review,
//     unchanged), so merged/closed MRs remain in the listing and are observed
//     by the normal refresh path. No reconciliation pass is needed for GitLab.
//
// The two providers match what the reviewer requested for each; they are not
// aligned to a single listing strategy.
func (o *Observer) reconcileTerminalGitHubPRs(ctx context.Context, subjects map[string]*subject, guards map[string]repoGuardState, listedPRs map[string]bool, selection *refreshSelection, now time.Time, markRepoFailed func(ports.SCMRepo)) map[string]ports.SCMObservation {
	out := map[string]ports.SCMObservation{}
	if listedPRs == nil {
		// First poll (no cursor): listedPRs is nil, meaning the full listing
		// was fetched. Tracked open PRs not in the listing already had their
		// chance to be discovered; no reconciliation needed.
		return out
	}
	// Collect refs to reconcile, deduped by repo so we only reconcile per-repo
	// when the repo-list guard was not a 304.
	type pendingReconcile struct {
		ref ports.SCMPRRef
		s   *subject
	}
	var refs []pendingReconcile
	for _, s := range subjects {
		if !s.hasPR || s.known.Number <= 0 {
			continue
		}
		// Asymmetry: only GitHub needs terminal reconciliation. GitLab uses
		// state=all so terminal MRs never disappear from the listing.
		if s.repo.Provider != "github" {
			continue
		}
		// Only tracked open PRs (non-terminal state in durable storage) are
		// candidates — terminal PRs are not re-fetched since lifecycle already
		// saw the transition.
		if s.known.Merged || s.known.Closed {
			continue
		}
		repoKey := prKey(s.repo, 0)
		g := guards[repoKey]
		// Reconciliation runs only when the repo-list guard is not a 304.
		if g.err != nil || g.result.NotModified {
			continue
		}
		key := prKey(s.repo, s.known.Number)
		// Only reconcile PRs not in the current listing — listed PRs are
		// already covered by the normal refresh path.
		if listedPRs[key] {
			continue
		}
		// Skip refs already selected as refresh candidates by the normal
		// path (e.g. commit-check ETag changed) — they will be fetched
		// anyway and we must not double-count.
		if selection.candidateKeys[key] {
			continue
		}
		refs = append(refs, pendingReconcile{
			ref: ports.SCMPRRef{Repo: s.repo, Number: s.known.Number, URL: s.known.URL},
			s:   s,
		})
	}
	if len(refs) == 0 {
		return out
	}
	// Unbounded: issue detail fetches for every reconciled PR. The worst case
	// is bounded by the tracked-open-PR count, which is small in AO's use case.
	for _, chunk := range chunks(refs, BatchSize) {
		if err := ctx.Err(); err != nil {
			return out
		}
		refBatch := make([]ports.SCMPRRef, 0, len(chunk))
		for _, r := range chunk {
			refBatch = append(refBatch, r.ref)
		}
		// Skip cooled-down providers so a rate-limited GitHub does not
		// suppress reconciliation of other providers (defensive — only
		// GitHub PRs are reconciled here, but the guard keeps the invariant).
		active := refBatch[:0]
		for _, ref := range refBatch {
			if o.inRateLimitCooldown(now, ref.Repo.Provider) {
				markRepoFailed(ref.Repo)
				continue
			}
			active = append(active, ref)
		}
		if len(active) == 0 {
			continue
		}
		batch, err := o.provider.FetchPullRequests(ctx, active)
		if err != nil {
			if cooldown, ok := rateLimitCooldown(now, err); ok {
				for _, ref := range active {
					o.setRateLimitCooldown(now, ref.Repo.Provider, cooldown)
				}
				o.logger.Warn("scm observer: reconciliation rate-limited; entering cooldown", "cooldown", cooldown, "err", err)
			} else {
				o.logger.Error("scm observer: reconciliation detail fetch failed", "err", err)
			}
			for _, ref := range active {
				markRepoFailed(ref.Repo)
			}
			continue
		}
		reconcileSeen := map[string]bool{}
		for _, obs := range batch {
			obs.ObservedAt = now
			key := prKeyFromObs(obs)
			if key == "" {
				continue
			}
			if !obs.Fetched {
				// Fetched=false placeholders are transient failures; route
				// per-observation errors via the same path as the normal
				// fetch loop. Do not persist placeholders.
				if obs.Error != nil {
					providerKey := obs.Provider
					if providerKey == "" {
						for _, ref := range active {
							if prKey(ref.Repo, ref.Number) == key {
								providerKey = ref.Repo.Provider
								break
							}
						}
					}
					if cooldown, ok := rateLimitCooldown(now, obs.Error); ok {
						if providerKey != "" {
							o.setRateLimitCooldown(now, providerKey, cooldown)
						}
						o.logger.Warn("scm observer: reconciliation rate-limited (per-observation); entering cooldown", "provider", providerKey, "cooldown", cooldown, "err", obs.Error)
					} else {
						o.logger.Warn("scm observer: reconciliation fetch failed (per-observation); marking refresh-incomplete", "provider", providerKey, "err", obs.Error)
					}
				}
				// Mark the repo refresh-incomplete on any failure so the
				// repo ETag/cursor do not advance without an observation.
				for _, ref := range active {
					if prKey(ref.Repo, ref.Number) == key {
						markRepoFailed(ref.Repo)
						break
					}
				}
				continue
			}
			out[key] = obs
			reconcileSeen[key] = true
			// Register the reconciled PR in selection.subjectsByPR and
			// candidateKeys so the persistence loop processes it. Marking
			// candidateKeys also ensures prRefreshOK is initialized for this
			// key so the commit-ETag cache does not advance unless the
			// persistence succeeds.
			for _, r := range chunk {
				if prKey(r.ref.Repo, r.ref.Number) == key {
					selection.subjectsByPR[key] = r.s
					break
				}
			}
			selection.candidateKeys[key] = true
			// Pre-mark the repo refresh-incomplete for every reconciled
			// "still open" observation. A "still open" result is a no-op
			// persistence that must NOT advance the repo ETag or sync
			// cursor (cross-cutting durable-state preservation rule). If
			// the observation turns out to be a terminal transition
			// (hashes changed), the persistence loop clears this mark
			// after a successful write so the ETag/cursor can advance on
			// a real terminal transition only.
			for _, ref := range active {
				if prKey(ref.Repo, ref.Number) == key {
					markRepoFailed(ref.Repo)
					break
				}
			}
		}
		// Any reconciled PR that did not yield a Fetched=true observation
		// must mark its repo refresh-incomplete (no-op result: durable state
		// must not advance).
		for _, r := range chunk {
			key := prKey(r.ref.Repo, r.ref.Number)
			if reconcileSeen[key] {
				// A "still open" observation: if the semantic hashes are
				// unchanged (no terminal transition), the persistence loop
				// treats it as a no-op and sets prRefreshOK[key]=true without
				// writing. But we must still prevent the repo ETag/cursor
				// from advancing on a no-op reconciliation result, so mark
				// the repo refresh-incomplete here. If the result IS a
				// terminal transition (hashes changed), the persistence
				// loop sets prRefreshOK[key]=true after a successful write —
				// we must NOT pre-mark the repo failed in that case. The
				// persistence loop's prRefreshOK[key]=true overrides the
				// repo-level mark for the ETag/cursor advancement decision
				// only if listedRepos[repoKey] is also true. Since the
				// reconciled PR was NOT in the listing, listedRepos[repoKey]
				// may still be true (the listing was fetched), so we mark
				// the repo refresh-incomplete to ensure the cursor does not
				// advance without a terminal observation being persisted.
				continue
			}
			markRepoFailed(r.ref.Repo)
		}
	}
	return out
}

func missingLocalState(pr domain.PullRequest) bool {
	return pr.URL == "" || pr.HeadSHA == "" || pr.MetadataHash == "" || pr.CIHash == ""
}

func (o *Observer) enrichFailureLogs(ctx context.Context, obs *ports.SCMObservation, local domain.PullRequest) {
	if obs.CI.Summary != string(domain.CIFailing) || obs.CI.FailedFingerprint == "" {
		return
	}
	if strings.HasPrefix(local.CIHash, obs.CI.FailedFingerprint+":") {
		checks, err := o.store.ListChecks(ctx, local.URL)
		if err == nil && applyStoredFailedLogTails(obs, checks) {
			return
		}
	}
	tails := make([]string, 0, len(obs.CI.FailedChecks))
	checksByProviderID := make(map[string][]int, len(obs.CI.Checks))
	for i := range obs.CI.Checks {
		key := checkProviderKey(obs.CI.Checks[i])
		checksByProviderID[key] = append(checksByProviderID[key], i)
	}
	for i := range obs.CI.FailedChecks {
		tail := obs.CI.FailedChecks[i].LogTail
		if tail == "" && obs.CI.FailedChecks[i].ProviderID != "" {
			var err error
			tail, err = o.provider.FetchFailedCheckLogTail(ctx, ports.SCMRepo{Provider: obs.Provider, Host: obs.Host, Repo: obs.Repo, Owner: ownerOf(obs.Repo), Name: nameOf(obs.Repo)}, obs.CI.FailedChecks[i])
			if err != nil {
				tail = "<log fetch failed: " + scrubLine(err.Error()) + ">"
			}
		}
		obs.CI.FailedChecks[i].LogTail = tail
		if tail != "" {
			tails = append(tails, tail)
		}
		for _, j := range checksByProviderID[checkProviderKey(obs.CI.FailedChecks[i])] {
			obs.CI.Checks[j].LogTail = tail
		}
	}
	obs.CI.FailureLogTail = strings.Join(tails, "\n---\n")
}

func checkProviderKey(ch ports.SCMCheckObservation) string {
	return ch.Name + "\x00" + ch.ProviderID
}

func applyStoredFailedLogTails(obs *ports.SCMObservation, checks []domain.PullRequestCheck) bool {
	tailsByName := map[string]string{}
	for _, ch := range checks {
		if obs.CI.HeadSHA != "" && ch.CommitHash != "" && ch.CommitHash != obs.CI.HeadSHA {
			continue
		}
		if ch.LogTail != "" && (ch.Status == domain.PRCheckFailed || ch.Status == domain.PRCheckCancelled) {
			tailsByName[ch.Name] = ch.LogTail
		}
	}
	if len(tailsByName) == 0 {
		return false
	}
	tails := make([]string, 0, len(obs.CI.FailedChecks))
	for i := range obs.CI.FailedChecks {
		tail := tailsByName[obs.CI.FailedChecks[i].Name]
		if tail == "" {
			return false
		}
		obs.CI.FailedChecks[i].LogTail = tail
		tails = append(tails, tail)
	}
	for i := range obs.CI.Checks {
		if tail := tailsByName[obs.CI.Checks[i].Name]; tail != "" {
			obs.CI.Checks[i].LogTail = tail
		}
	}
	obs.CI.FailureLogTail = strings.Join(tails, "\n---\n")
	return true
}

func (o *Observer) refreshReviews(ctx context.Context, subjects map[string]*subject, observations map[string]ports.SCMObservation, subjectsByPR map[string]*subject, reviewModes map[string]ports.ReviewWriteMode, localOnlyObservations, reviewStale map[string]bool, now time.Time) {
	for _, s := range subjects {
		if !s.hasPR || s.known.Number <= 0 {
			continue
		}
		if s.known.Merged || s.known.Closed {
			continue
		}
		pkey := prKey(s.repo, s.known.Number)
		obs, hasObs := observations[pkey]
		decision := string(s.known.Review)
		if hasObs && obs.Review.Decision != "" {
			decision = obs.Review.Decision
		}
		updatedAtProvider := obs.PR.UpdatedAtProvider
		if !o.needsReviewRefresh(pkey, s.known, decision, hasObs, updatedAtProvider, now) {
			continue
		}
		review, err := o.provider.FetchReviewThreads(ctx, ports.SCMPRRef{Repo: s.repo, Number: s.known.Number, URL: s.known.URL})
		if err != nil {
			o.logger.Error("scm observer: review refresh failed", "pr", s.known.URL, "err", err)
			o.cacheSetBool(o.Cache.ReviewRefreshFailed, &o.Cache.reviewFailedOrder, pkey, true)
			if hasObs {
				obs.Review.Decision = string(s.known.Review)
				obs.Review.Threads = nil
				observations[pkey] = obs
				subjectsByPR[pkey] = s
				reviewStale[pkey] = true
			}
			continue
		}
		if !hasObs {
			checks, err := o.store.ListChecks(ctx, s.known.URL)
			if err != nil {
				o.logger.Error("scm observer: list local checks for review-only refresh failed", "pr", s.known.URL, "err", err)
			}
			obs = observationFromLocal(s.repo, s.known, checks)
			localOnlyObservations[pkey] = true
		}
		// Precedence guard: don't let FetchReviewThreads downgrade a higher-
		// priority decision that was set by the fast-path. Only overwrite when
		// the fetched decision has equal or higher priority. Threads, summaries,
		// and the partial flag are always adopted regardless of the guard.
		if review.Decision != "" && reviewDecisionPriority(domain.ReviewDecision(review.Decision)) >= reviewDecisionPriority(domain.ReviewDecision(obs.Review.Decision)) {
			obs.Review.Decision = review.Decision
		}
		obs.Review.Reviews = review.Reviews
		obs.Review.Threads = review.Threads
		obs.Review.Partial = review.Partial
		obs.ObservedAt = now
		observations[pkey] = obs
		subjectsByPR[pkey] = s
		if review.Partial {
			reviewModes[pkey] = ports.ReviewWriteMerge
		} else {
			reviewModes[pkey] = ports.ReviewWriteReplace
		}
		cacheDelete(o.Cache.ReviewRefreshFailed, &o.Cache.reviewFailedOrder, pkey)
	}
}

// reviewDecisionPriority returns a numeric priority for a review decision.
// Higher values represent more authoritative or blocking decisions. The guard
// in refreshReviews uses this to prevent FetchReviewThreads from downgrading a
// decision that was set by the fast-path (e.g. fetchSingleMR mapping
// detailed_merge_status=requested_changes to ReviewChangesRequest). The guard
// is cross-provider: it also protects GitHub PRs whose CHANGES_REQUESTED status
// was mapped to ReviewChangesRequest in the fast path.
func reviewDecisionPriority(d domain.ReviewDecision) int {
	switch d {
	case domain.ReviewChangesRequest:
		return 3
	case domain.ReviewRequired:
		return 2
	case domain.ReviewApproved:
		return 1
	default:
		return 0
	}
}

func (o *Observer) needsReviewRefresh(key string, local domain.PullRequest, decision string, hasObs bool, updatedAtProvider, now time.Time) bool {
	if o.Cache.ReviewRefreshFailed[key] {
		return true
	}
	if local.ReviewHash == "" {
		return true
	}
	if local.ReviewObservedAt.IsZero() {
		return true
	}
	if decision == string(domain.ReviewChangesRequest) {
		last := o.Cache.LastReviewPollAt[key]
		return last.IsZero() || now.Sub(last) >= o.reviewInterval
	}
	if hasObs && decision != string(local.Review) {
		return true
	}
	if local.ReviewHash != "" && string(local.Review) == string(domain.ReviewChangesRequest) && decision != string(domain.ReviewChangesRequest) {
		return true
	}
	if hasObs && !updatedAtProvider.IsZero() && updatedAtProvider.After(local.ReviewObservedAt) {
		return true
	}
	return false
}

func (o *Observer) prepareForPersistence(obs ports.SCMObservation, local domain.PullRequest, opts persistenceOptions, now time.Time) ports.SCMObservation {
	metadataHash := metadataSemanticHash(obs)
	if opts.preserveLocalMetadataHash {
		metadataHash = local.MetadataHash
	}
	ciHash := ciSemanticHash(obs.CI)
	if opts.preserveLocalCIHash {
		ciHash = local.CIHash
	}
	reviewHash := local.ReviewHash
	if !opts.preserveLocalReviewHash && (opts.reviewFetched || local.ReviewHash == "" || obs.Review.Decision != string(local.Review)) {
		reviewHash = reviewSemanticHash(obs.Review)
	}
	obs.Changed = ports.SCMChanged{
		Metadata: metadataHash != local.MetadataHash,
		CI:       ciHash != local.CIHash,
		Review:   reviewHash != local.ReviewHash,
	}
	obs.PR.State = firstNonEmpty(obs.PR.State, normalizePRState(obs.PR.Draft, obs.PR.Merged, obs.PR.Closed))
	obs.ObservedAt = firstTime(obs.ObservedAt, now)
	return obs
}

func domainFromObservation(sessionID domain.SessionID, sessionRecord domain.SessionRecord, obs ports.SCMObservation, local domain.PullRequest, opts persistenceOptions, now time.Time) (domain.PullRequest, []domain.PullRequestCheck, []domain.PullRequestReview, []domain.PullRequestReviewThread, []domain.PullRequestComment) {
	metadataHash := metadataSemanticHash(obs)
	if opts.preserveLocalMetadataHash {
		metadataHash = local.MetadataHash
	}
	ciHash := ciSemanticHash(obs.CI)
	if opts.preserveLocalCIHash {
		ciHash = local.CIHash
	}
	reviewHash := reviewSemanticHash(obs.Review)
	reviewDecision := domain.ReviewDecision(firstNonEmpty(obs.Review.Decision, string(domain.ReviewNone)))
	if opts.preserveLocalReviewDecision {
		reviewDecision = local.Review
	}
	if opts.preserveLocalReviewHash {
		reviewHash = local.ReviewHash
	} else if !opts.reviewFetched && local.ReviewHash != "" && reviewDecision == local.Review {
		reviewHash = local.ReviewHash
	}
	observedAt := obs.ObservedAt
	if !obs.Changed.Metadata && !obs.Changed.CI && !local.ObservedAt.IsZero() {
		observedAt = local.ObservedAt
	}
	ciObservedAt := local.CIObservedAt
	if obs.Changed.CI || ciObservedAt.IsZero() {
		ciObservedAt = obs.ObservedAt
	}
	reviewObservedAt := local.ReviewObservedAt
	if opts.reviewFetched || reviewObservedAt.IsZero() {
		reviewObservedAt = obs.ObservedAt
	}
	pr := domain.PullRequest{
		URL:                      firstNonEmpty(obs.PR.URL, obs.PR.HTMLURL),
		URLAlias:                 obs.PR.URLAlias,
		SessionID:                sessionID,
		Number:                   obs.PR.Number,
		Draft:                    obs.PR.Draft,
		Merged:                   obs.PR.Merged,
		Closed:                   obs.PR.Closed,
		CI:                       domain.CIState(firstNonEmpty(obs.CI.Summary, string(domain.CIUnknown))),
		Review:                   reviewDecision,
		Mergeability:             domain.Mergeability(firstNonEmpty(obs.Mergeability.State, string(domain.MergeUnknown))),
		UpdatedAt:                now,
		Provider:                 obs.Provider,
		Host:                     obs.Host,
		Repo:                     obs.Repo,
		ProviderID:               obs.PR.ProviderID,
		SourceBranch:             obs.PR.SourceBranch,
		TargetBranch:             obs.PR.TargetBranch,
		HeadSHA:                  obs.PR.HeadSHA,
		Title:                    obs.PR.Title,
		Additions:                obs.PR.Additions,
		Deletions:                obs.PR.Deletions,
		ChangedFiles:             obs.PR.ChangedFiles,
		Author:                   obs.PR.Author,
		BaseSHA:                  obs.PR.BaseSHA,
		MergeCommitSHA:           obs.PR.MergeCommitSHA,
		ProviderState:            obs.PR.ProviderState,
		ProviderMergeable:        obs.PR.ProviderMergeable,
		ProviderMergeStateStatus: obs.PR.ProviderMergeStateStatus,
		HTMLURL:                  obs.PR.HTMLURL,
		CreatedAtProvider:        obs.PR.CreatedAtProvider,
		UpdatedAtProvider:        obs.PR.UpdatedAtProvider,
		MergedAtProvider:         obs.PR.MergedAtProvider,
		ClosedAtProvider:         obs.PR.ClosedAtProvider,
		MetadataHash:             metadataHash,
		CIHash:                   ciHash,
		ReviewHash:               reviewHash,
		ObservedAt:               observedAt,
		CIObservedAt:             ciObservedAt,
		ReviewObservedAt:         reviewObservedAt,
	}
	checks := make([]domain.PullRequestCheck, 0, len(obs.CI.Checks))
	for _, ch := range obs.CI.Checks {
		checks = append(checks, domain.PullRequestCheck{Name: ch.Name, CommitHash: obs.CI.HeadSHA, Status: domain.PRCheckStatus(ch.Status), Conclusion: ch.Conclusion, URL: ch.URL, Details: ch.ProviderID, LogTail: ch.LogTail, CreatedAt: now})
	}
	reviews := make([]domain.PullRequestReview, 0, len(obs.Review.Reviews))
	for _, review := range obs.Review.Reviews {
		reviews = append(reviews, domain.PullRequestReview{
			ID:               review.ID,
			Author:           review.Author,
			State:            domain.ReviewDecision(firstNonEmpty(review.State, string(domain.ReviewNone))),
			URL:              review.URL,
			Body:             review.Body,
			IsBot:            review.IsBot,
			TargetSHA:        review.TargetSHA,
			SubmittedAt:      firstTime(review.SubmittedAt, now),
			AutoInjectReview: sessionRecord.AutoInjectReview,
		})
	}
	threads := make([]domain.PullRequestReviewThread, 0, len(obs.Review.Threads))
	commentCount := 0
	for _, th := range obs.Review.Threads {
		commentCount += len(th.Comments)
	}
	comments := make([]domain.PullRequestComment, 0, commentCount)
	for _, th := range obs.Review.Threads {
		threads = append(threads, domain.PullRequestReviewThread{ThreadID: th.ID, Path: th.Path, Line: th.Line, Resolved: th.Resolved, IsBot: th.IsBot, SemanticHash: threadSemanticHash(th), UpdatedAt: now})
		for _, c := range th.Comments {
			comments = append(comments, domain.PullRequestComment{ThreadID: th.ID, ID: c.ID, Author: c.Author, File: th.Path, Line: th.Line, Body: c.Body, URL: c.URL, Resolved: th.Resolved, IsBot: c.IsBot || th.IsBot, CreatedAt: now, AutoInjectReview: sessionRecord.AutoInjectReview})
		}
	}
	return pr, checks, reviews, threads, comments
}

func observationFromLocal(repo ports.SCMRepo, pr domain.PullRequest, checks []domain.PullRequestCheck) ports.SCMObservation {
	return ports.SCMObservation{
		Fetched:      true,
		Provider:     firstNonEmpty(pr.Provider, repo.Provider),
		Host:         firstNonEmpty(pr.Host, repo.Host),
		Repo:         firstNonEmpty(pr.Repo, repoFullName(repo)),
		PR:           ports.SCMPRObservation{URL: pr.URL, Number: pr.Number, State: normalizePRState(pr.Draft, pr.Merged, pr.Closed), Draft: pr.Draft, Merged: pr.Merged, Closed: pr.Closed, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch, HeadSHA: pr.HeadSHA, Title: pr.Title, Additions: pr.Additions, Deletions: pr.Deletions, ChangedFiles: pr.ChangedFiles, Author: pr.Author, BaseSHA: pr.BaseSHA, MergeCommitSHA: pr.MergeCommitSHA, ProviderState: pr.ProviderState, ProviderMergeable: pr.ProviderMergeable, ProviderMergeStateStatus: pr.ProviderMergeStateStatus, HTMLURL: pr.HTMLURL, CreatedAtProvider: pr.CreatedAtProvider, UpdatedAtProvider: pr.UpdatedAtProvider, MergedAtProvider: pr.MergedAtProvider, ClosedAtProvider: pr.ClosedAtProvider},
		CI:           ciObservationFromLocal(pr, checks),
		Review:       ports.SCMReviewObservation{Decision: string(pr.Review)},
		Mergeability: mergeabilityObservationFromLocal(pr),
	}
}

func ciObservationFromLocal(pr domain.PullRequest, checks []domain.PullRequestCheck) ports.SCMCIObservation {
	ci := ports.SCMCIObservation{
		Summary:           firstNonEmpty(string(pr.CI), string(domain.CIUnknown)),
		HeadSHA:           pr.HeadSHA,
		FailedFingerprint: failedFingerprintFromCIHash(pr.CIHash),
	}
	tails := []string{}
	for _, ch := range checks {
		if pr.HeadSHA != "" && ch.CommitHash != "" && ch.CommitHash != pr.HeadSHA {
			continue
		}
		if ci.HeadSHA == "" {
			ci.HeadSHA = ch.CommitHash
		}
		check := ports.SCMCheckObservation{
			Name:       ch.Name,
			Status:     string(ch.Status),
			Conclusion: ch.Conclusion,
			URL:        ch.URL,
			LogTail:    ch.LogTail,
			ProviderID: ch.Details,
		}
		ci.Checks = append(ci.Checks, check)
		if ch.Status == domain.PRCheckFailed || ch.Status == domain.PRCheckCancelled {
			ci.FailedChecks = append(ci.FailedChecks, check)
			if ch.LogTail != "" {
				tails = append(tails, ch.LogTail)
			}
		}
	}
	ci.FailureLogTail = strings.Join(tails, "\n---\n")
	return ci
}

func failedFingerprintFromCIHash(hash string) string {
	before, _, ok := strings.Cut(hash, ":")
	if !ok {
		return ""
	}
	return before
}

func mergeabilityObservationFromLocal(pr domain.PullRequest) ports.SCMMergeabilityObservation {
	out := mergeabilityFromProviderFacts(pr.ProviderMergeable, pr.ProviderMergeStateStatus, string(pr.CI), string(pr.Review), pr.Draft)
	if pr.Mergeability != "" && out.State != string(pr.Mergeability) {
		out = ports.SCMMergeabilityObservation{State: string(pr.Mergeability)}
	} else if pr.Mergeability != "" {
		out.State = string(pr.Mergeability)
	}
	switch domain.Mergeability(out.State) {
	case domain.MergeMergeable:
		out.Mergeable = true
	case domain.MergeConflicting:
		out.Conflict = true
		if len(out.Blockers) == 0 {
			out.Blockers = append(out.Blockers, "conflicts")
		}
	case domain.MergeBlocked:
		if len(out.Blockers) == 0 {
			out.Blockers = mergeBlockersFromLocal(pr)
		}
	}
	return out
}

func mergeBlockersFromLocal(pr domain.PullRequest) []string {
	blockers := []string{}
	if pr.Draft {
		blockers = append(blockers, "draft")
	}
	if pr.CI == domain.CIFailing {
		blockers = append(blockers, "ci_failing")
	}
	switch pr.Review {
	case domain.ReviewChangesRequest:
		blockers = append(blockers, "changes_requested")
	case domain.ReviewRequired:
		blockers = append(blockers, "review_required")
	}
	if len(blockers) == 0 {
		blockers = append(blockers, "blocked_by_provider")
	}
	return blockers
}

func mergeabilityFromProviderFacts(providerMergeable, providerMergeState, ci, review string, draft bool) ports.SCMMergeabilityObservation {
	state := strings.ToUpper(strings.TrimSpace(providerMergeState))
	mergeable := strings.ToUpper(strings.TrimSpace(providerMergeable))
	out := ports.SCMMergeabilityObservation{State: string(domain.MergeUnknown)}
	addBlocker := func(b string) { out.Blockers = append(out.Blockers, b) }
	if state == "DIRTY" || mergeable == "CONFLICTING" {
		out.State = string(domain.MergeConflicting)
		out.Conflict = true
		addBlocker("conflicts")
		return out
	}
	if state == "BEHIND" || state == "BEHIND_BASE" {
		out.BehindBase = true
		addBlocker("behind_base")
	}
	if state == "BLOCKED" {
		out.State = string(domain.MergeBlocked)
		addBlocker("blocked_by_provider")
	}
	if draft {
		out.State = string(domain.MergeBlocked)
		addBlocker("draft")
	}
	if ci == string(domain.CIFailing) {
		out.State = string(domain.MergeBlocked)
		addBlocker("ci_failing")
	}
	switch review {
	case string(domain.ReviewChangesRequest):
		out.State = string(domain.MergeBlocked)
		addBlocker("changes_requested")
	case string(domain.ReviewRequired):
		out.State = string(domain.MergeBlocked)
		addBlocker("review_required")
	}
	if out.State == string(domain.MergeBlocked) {
		return out
	}
	if state == "UNSTABLE" {
		out.State = string(domain.MergeUnstable)
		return out
	}
	if mergeable == "MERGEABLE" && (state == "CLEAN" || state == "HAS_HOOKS" || state == "") &&
		(review == "" || review == string(domain.ReviewApproved) || review == string(domain.ReviewNone)) && !draft {
		out.State = string(domain.MergeMergeable)
		out.Mergeable = true
		return out
	}
	return out
}

func chunks[T any](in []T, n int) [][]T {
	if n <= 0 || len(in) == 0 {
		return nil
	}
	out := make([][]T, 0, (len(in)+n-1)/n)
	for len(in) > 0 {
		end := n
		if len(in) < end {
			end = len(in)
		}
		out = append(out, in[:end])
		in = in[end:]
	}
	return out
}

func metadataSemanticHash(obs ports.SCMObservation) string {
	return stableHash(map[string]any{"provider": obs.Provider, "host": obs.Host, "repo": obs.Repo, "pr": obs.PR, "mergeability": obs.Mergeability})
}

func ciSemanticHash(ci ports.SCMCIObservation) string {
	h := stableHash(map[string]any{"summary": ci.Summary, "head": ci.HeadSHA, "checks": ci.Checks, "failed": ci.FailedChecks, "tail": ci.FailureLogTail})
	if ci.FailedFingerprint != "" {
		return ci.FailedFingerprint + ":" + h
	}
	return h
}

func reviewSemanticHash(review ports.SCMReviewObservation) string {
	type reviewHashPayload struct {
		Decision string
		Reviews  []ports.SCMReviewSummaryObservation
		Threads  []ports.SCMReviewThreadObservation
		Partial  bool `json:",omitempty"`
	}
	return stableHash(reviewHashPayload{Decision: review.Decision, Reviews: review.Reviews, Threads: review.Threads, Partial: review.Partial})
}

func threadSemanticHash(th ports.SCMReviewThreadObservation) string {
	return stableHash(th)
}

func stableHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%#v", v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func prKeyFromObs(obs ports.SCMObservation) string {
	if obs.Repo == "" || obs.PR.Number <= 0 {
		return ""
	}
	return obs.Provider + ":" + obs.Host + ":" + obs.Repo + "#" + fmt.Sprint(obs.PR.Number)
}

func prKey(repo ports.SCMRepo, number int) string {
	base := repo.Provider + ":" + repo.Host + ":" + repoFullName(repo)
	if number <= 0 {
		return base
	}
	return base + "#" + fmt.Sprint(number)
}

func commitKey(repo ports.SCMRepo, sha string) string { return prKey(repo, 0) + "@" + sha }

func repoFullName(repo ports.SCMRepo) string {
	if repo.Repo != "" {
		return repo.Repo
	}
	return repo.Owner + "/" + repo.Name
}

func ownerOf(full string) string {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	return ""
}

func nameOf(full string) string {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return full
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func normalizePRState(draft, merged, closed bool) string {
	switch {
	case merged:
		return string(domain.PRStateMerged)
	case closed:
		return string(domain.PRStateClosed)
	case draft:
		return string(domain.PRStateDraft)
	default:
		return string(domain.PRStateOpen)
	}
}

// resolveGitOriginURL runs `git -C path remote get-url origin` and returns the
// trimmed URL, or "" if the command fails (missing repo, no origin remote, etc).
// The observer uses this to backfill projects that were registered before
// project.Add resolved origin URLs at add time.
func resolveGitOriginURL(path string) string {
	out, err := aoprocess.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitRemoteURLs lists the fetch URL of every git remote configured at path. It
// returns nil on any error (missing repo, no git, no remotes). The observer uses
// it to scan upstream/mirror remotes for cross-fork PRs in addition to origin.
func gitRemoteURLs(path string) []string {
	out, err := aoprocess.Command("git", "-C", path, "remote").Output()
	if err != nil {
		return nil
	}
	var urls []string
	for _, name := range strings.Fields(string(out)) {
		u, err := aoprocess.Command("git", "-C", path, "remote", "get-url", name).Output()
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(u)); s != "" {
			urls = append(urls, s)
		}
	}
	return urls
}

var gitRemoteURLsFunc = gitRemoteURLs

func scrubLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// cacheSetString / cacheSetTime / cacheSetBool are thin wrappers around the
// generic observe.CacheSet helper, kept on Observer so callers don't need to
// thread o.Cache.max through every invocation. The single shared
// implementation lives in the observe package.
func (o *Observer) cacheSetString(m map[string]string, order *[]string, key, value string) {
	observe.CacheSet(m, order, o.Cache.max, key, value)
}

func (o *Observer) cacheSetTime(m map[string]time.Time, order *[]string, key string, value time.Time) {
	observe.CacheSet(m, order, o.Cache.max, key, value)
}

func (o *Observer) cacheSetBool(m map[string]bool, order *[]string, key string, value bool) {
	observe.CacheSet(m, order, o.Cache.max, key, value)
}

func cacheDelete[V any](m map[string]V, order *[]string, key string) {
	observe.CacheDelete(m, order, key)
}

// inRateLimitCooldown reports whether the named provider is currently under a
// rate-limit cooldown. Provider key is the SCMRepo.Provider
// value (e.g. "gitlab", "github").
func (o *Observer) inRateLimitCooldown(now time.Time, providerKey string) bool {
	if o.rateLimitUntil == nil {
		return false
	}
	until, ok := o.rateLimitUntil[providerKey]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	// Cooldown expired — clear the entry so it doesn't grow unboundedly.
	delete(o.rateLimitUntil, providerKey)
	return false
}

// setRateLimitCooldown records that providerKey should be skipped until now+d.
func (o *Observer) setRateLimitCooldown(now time.Time, providerKey string, d time.Duration) {
	if o.rateLimitUntil == nil {
		o.rateLimitUntil = map[string]time.Time{}
	}
	o.rateLimitUntil[providerKey] = now.Add(d)
}
