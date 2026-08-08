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
)

// Provider is the normalized SCM provider contract used by the observer.
type Provider interface {
	ParseRepository(remote string) (ports.SCMRepo, bool)
	RepoPRListGuard(ctx context.Context, repo ports.SCMRepo, etag string) (ports.SCMGuardResult, error)
	ListOpenPRsByRepo(ctx context.Context, repo ports.SCMRepo) ([]ports.SCMPRObservation, error)
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
	// Cache holds bounded in-memory provider ETags and review poll timestamps.
	Cache ObserverCache
}

// New constructs an Observer with default cadence/cache settings for zero
// values in cfg.
func New(provider Provider, store Store, lifecycle Lifecycle, cfg Config) *Observer {
	o := &Observer{provider: provider, store: store, lifecycle: lifecycle, tick: cfg.Tick, reviewInterval: cfg.ReviewInterval, clock: cfg.Clock, logger: cfg.Logger, identityResolver: cfg.IdentityResolver, Cache: newCache(cfg.CacheMax)}
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
	markRepoRefreshFailed := func(repo ports.SCMRepo) {
		key := prKey(repo, 0)
		if _, ok := repoRefreshOK[key]; ok {
			repoRefreshOK[key] = false
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.discoverNewPRs(ctx, sessionRepos, subjects, repoGuards, now, markRepoRefreshFailed)
	if err := ctx.Err(); err != nil {
		return err
	}

	selection := o.selectRefreshCandidates(ctx, subjects, repoGuards, markRepoRefreshFailed, now)
	observations := map[string]ports.SCMObservation{}
	prRefreshOK := map[string]bool{}
	for key := range selection.candidateKeys {
		prRefreshOK[key] = false
	}
	for _, chunk := range chunks(selection.refs, BatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := o.provider.FetchPullRequests(ctx, chunk)
		if err != nil {
			o.logger.Error("scm observer: GraphQL PR batch failed", "err", err)
			for _, ref := range chunk {
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
			observations[key] = obs
			chunkSeen[key] = true
		}
		for _, ref := range chunk {
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
		subj, ok := selection.subjectsByPR[key]
		if !ok {
			continue
		}
		local := subj.known
		reviewMode := reviewModes[key]
		opts := persistenceOptions{
			reviewFetched:               reviewMode != ports.ReviewWritePreserve,
			preserveLocalMetadataHash:   localOnlyObservations[key],
			preserveLocalCIHash:         localOnlyObservations[key],
			preserveLocalReviewHash:     reviewStale[key],
			preserveLocalReviewDecision: reviewStale[key],
		}
		prepared := o.prepareForPersistence(obs, local, opts, now)
		snapshotReviewInjectionPolicy(&prepared, subj.session.AutoInjectReview)
		if !prepared.Changed.Metadata && !prepared.Changed.CI && !prepared.Changed.Review {
			prRefreshOK[key] = true
			continue
		}
		finalPR, finalChecks, finalReviews, finalThreads, finalComments := domainFromObservation(subj.session.ID, prepared, local, opts, now)
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
			pr, checks, reviews, threads, comments = domainFromObservation(subj.session.ID, prepared, local, pendingOpts, now)
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
	for key, ok := range repoRefreshOK {
		if !ok {
			continue
		}
		if etag := repoGuards[key].result.ETag; etag != "" {
			o.cacheSetString(o.Cache.RepoPRListETag, &o.Cache.repoOrder, key, etag)
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
func (o *Observer) discoverNewPRs(ctx context.Context, sessionRepos []sessionRepo, subjects map[string]*subject, guards map[string]repoGuardState, now time.Time, markRepoFailed func(ports.SCMRepo)) {
	identity, identityKnown := o.authenticatedIdentity(ctx)
	byRepo := map[string][]sessionRepo{}
	repos := map[string]ports.SCMRepo{}
	for _, sr := range sessionRepos {
		key := prKey(sr.repo, 0)
		byRepo[key] = append(byRepo[key], sr)
		repos[key] = sr.repo
	}
	for repoKey, repo := range repos {
		g := guards[repoKey]
		if g.err != nil {
			continue
		}
		if g.result.NotModified && g.hadETag {
			continue
		}
		pulls, err := o.provider.ListOpenPRsByRepo(ctx, repo)
		if err != nil {
			o.logger.Debug("scm observer: open PR list failed", "repo", repoFullName(repo), "err", err)
			if markRepoFailed != nil && !errors.Is(err, ports.ErrSCMNotFound) {
				markRepoFailed(repo)
			}
			continue
		}
		for _, pr := range pulls {
			if pr.Number <= 0 || pr.SourceBranch == "" {
				continue
			}
			if identityKnown && !strings.EqualFold(strings.TrimSpace(pr.Author), identity.Login) {
				continue
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

func (o *Observer) selectRefreshCandidates(ctx context.Context, subjects map[string]*subject, guards map[string]repoGuardState, markRepoFailed func(ports.SCMRepo), now time.Time) refreshSelection {
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
		g := guards[prKey(s.repo, 0)]
		if g.err == nil && !g.result.NotModified {
			candidate = true
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
			} else if !res.NotModified {
				candidate = true
				if res.ETag != "" {
					selection.commitETags[key] = pendingCacheString{key: commitKey, value: res.ETag}
				}
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
			last := o.Cache.LastPRFetchAt[key]
			if last.IsZero() || now.Sub(last) > DefaultPRMaxAge {
				candidate = true
			}
		}
		if candidate {
			selection.refs = append(selection.refs, ports.SCMPRRef{Repo: s.repo, Number: s.known.Number, URL: s.known.URL})
			selection.candidateKeys[key] = true
		}
	}
	return selection
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
		if !o.needsReviewRefresh(pkey, s.known, decision, hasObs, now) {
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
		if review.Decision != "" {
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

func (o *Observer) needsReviewRefresh(key string, local domain.PullRequest, decision string, hasObs bool, now time.Time) bool {
	if o.Cache.ReviewRefreshFailed[key] {
		return true
	}
	if local.ReviewHash == "" {
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

func snapshotReviewInjectionPolicy(obs *ports.SCMObservation, autoInjectReview bool) {
	obs.AutoInjectReview = autoInjectReview
	for i := range obs.Review.Reviews {
		obs.Review.Reviews[i].AutoInjectReview = autoInjectReview
	}
	for i := range obs.Review.Threads {
		for j := range obs.Review.Threads[i].Comments {
			obs.Review.Threads[i].Comments[j].AutoInjectReview = autoInjectReview
		}
	}
}

func domainFromObservation(sessionID domain.SessionID, obs ports.SCMObservation, local domain.PullRequest, opts persistenceOptions, now time.Time) (domain.PullRequest, []domain.PullRequestCheck, []domain.PullRequestReview, []domain.PullRequestReviewThread, []domain.PullRequestComment) {
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
			SubmittedAt:      firstTime(review.SubmittedAt, now),
			AutoInjectReview: review.AutoInjectReview,
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
			comments = append(comments, domain.PullRequestComment{ThreadID: th.ID, ID: c.ID, Author: c.Author, File: th.Path, Line: th.Line, Body: c.Body, URL: c.URL, Resolved: th.Resolved, IsBot: c.IsBot || th.IsBot, CreatedAt: now, AutoInjectReview: c.AutoInjectReview})
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
