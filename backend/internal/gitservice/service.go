// Package gitservice owns the privileged, approval-gated Git push boundary.
package gitservice

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const defaultApprovalTTL = 5 * time.Minute

var (
	ErrNotFound       = errors.New("git push approval not found")
	ErrNotApprovable  = errors.New("git push approval is not pending or has expired")
	ErrNotConsumable  = errors.New("git push approval has already been consumed or is no longer valid")
	ErrRepositoryRace = errors.New("repository state no longer matches the approval")
)

type Store interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
	CreatePushApproval(context.Context, domain.PushApproval) (domain.PushApproval, error)
	GetPushApproval(context.Context, string) (domain.PushApproval, bool, error)
	ApprovePushApproval(context.Context, string, string, time.Time) (domain.PushApproval, bool, error)
	ClaimPushApproval(context.Context, string, time.Time) (domain.PushApproval, bool, error)
	RevokePushApproval(context.Context, string) (domain.PushApproval, bool, error)
	ExpirePushApproval(context.Context, string, time.Time) (domain.PushApproval, bool, error)
	CompletePushApproval(context.Context, string, string) (domain.PushApproval, bool, error)
	FailPushApproval(context.Context, string, string, string) (domain.PushApproval, bool, error)
	CreateGitActionAudit(context.Context, domain.GitActionAudit) error
}

type Runner interface {
	Run(context.Context, string, []string, []string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, args, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

type Service struct {
	store Store
	run   Runner
	now   func() time.Time
	ttl   time.Duration
	// Tests use local bare repositories. Production never enables this because
	// pushing to a Worker-controlled local repository could execute its receive
	// hooks under the privileged Broker identity.
	allowLocalRemotes bool
}

func New(store Store) *Service {
	return &Service{store: store, run: execRunner{}, now: time.Now, ttl: defaultApprovalTTL}
}

type PrepareInput struct {
	SessionID   string `json:"sessionId"`
	Remote      string `json:"remote"`
	RequestedBy string `json:"requestedBy"`
}

type Proposal struct {
	ApprovalID   string    `json:"approvalId"`
	ProjectID    string    `json:"projectId"`
	ProjectName  string    `json:"projectName"`
	Repository   string    `json:"repository"`
	Remote       string    `json:"remote"`
	RemoteURL    string    `json:"remoteUrl"`
	Branch       string    `json:"branch"`
	HeadSHA      string    `json:"headSha"`
	Commits      []string  `json:"commits"`
	DiffSummary  string    `json:"diffSummary"`
	ChangedFiles []string  `json:"changedFiles"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type PushResult struct {
	ApprovalID string                    `json:"approvalId"`
	Status     domain.PushApprovalStatus `json:"status"`
	Result     string                    `json:"result"`
}

type repositoryState struct {
	root, remote, remoteURL, branch, head string
}

func (s *Service) Prepare(ctx context.Context, in PrepareInput) (Proposal, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return Proposal{}, errors.New("sessionId is required")
	}
	session, ok, err := s.store.GetSession(ctx, domain.SessionID(in.SessionID))
	if err != nil {
		return Proposal{}, err
	}
	if !ok {
		return Proposal{}, errors.New("session not found")
	}
	if session.Kind != domain.KindWorker {
		return Proposal{}, errors.New("only worker sessions may request a push")
	}
	dir := session.Metadata.WorkspaceRepoPath
	if dir == "" {
		dir = session.Metadata.WorkspacePath
	}
	remote := strings.TrimSpace(in.Remote)
	if remote == "" {
		remote = "origin"
	}
	state, err := s.inspect(ctx, dir, remote)
	if err != nil {
		return Proposal{}, err
	}
	created := s.now().UTC()
	rec, err := s.store.CreatePushApproval(ctx, domain.PushApproval{
		ID: uuid.NewString(), ProjectID: session.ProjectID, SessionID: session.ID,
		Repository: state.root, Remote: state.remote, RemoteURL: state.remoteURL,
		Branch: state.branch, ExpectedHeadSHA: state.head, CreatedAt: created,
		ExpiresAt: created.Add(s.ttl), Status: domain.PushApprovalPending,
	})
	if err != nil {
		return Proposal{}, err
	}
	requestedBy := strings.TrimSpace(in.RequestedBy)
	if requestedBy == "" {
		requestedBy = "desktop-user"
	}
	if err := s.audit(ctx, rec, domain.GitActionPushRequested, requestedBy, "git-broker", "pending", ""); err != nil {
		_, _, _ = s.store.FailPushApproval(ctx, rec.ID, "audit_failed", err.Error())
		return Proposal{}, err
	}
	project, _, _ := s.store.GetProject(ctx, string(session.ProjectID))
	commits, _ := s.lines(ctx, state.root, "log", "--oneline", "--decorate=no", "-20", "@{upstream}..HEAD")
	if len(commits) == 0 {
		commits, _ = s.lines(ctx, state.root, "log", "--oneline", "--decorate=no", "-20", "HEAD")
	}
	diff, _ := s.run.Run(ctx, state.root, []string{"diff", "--stat", "@{upstream}...HEAD"}, brokerEnvironment())
	files, _ := s.lines(ctx, state.root, "diff", "--name-only", "@{upstream}...HEAD")
	return Proposal{ApprovalID: rec.ID, ProjectID: string(rec.ProjectID), ProjectName: project.DisplayName,
		Repository: rec.Repository, Remote: rec.Remote, RemoteURL: rec.RemoteURL, Branch: rec.Branch,
		HeadSHA: rec.ExpectedHeadSHA, Commits: commits, DiffSummary: diff, ChangedFiles: files, ExpiresAt: rec.ExpiresAt}, nil
}

func (s *Service) ApproveAndPush(ctx context.Context, approvalID, approvedBy string) (PushResult, error) {
	now := s.now().UTC()
	rec, ok, err := s.store.GetPushApproval(ctx, approvalID)
	if err != nil {
		return PushResult{}, err
	}
	if !ok {
		return PushResult{}, ErrNotFound
	}
	if !rec.ExpiresAt.After(now) {
		_, _, _ = s.store.ExpirePushApproval(ctx, rec.ID, now)
		_ = s.audit(ctx, rec, domain.GitActionPushRejected, approvedBy, "git-broker", "expired", "approval expired")
		return PushResult{}, ErrNotApprovable
	}
	approvedBy = strings.TrimSpace(approvedBy)
	if approvedBy == "" {
		approvedBy = "desktop-user"
	}
	rec, ok, err = s.store.ApprovePushApproval(ctx, rec.ID, approvedBy, now)
	if err != nil {
		return PushResult{}, err
	}
	if !ok {
		return PushResult{}, ErrNotApprovable
	}
	if err := s.audit(ctx, rec, domain.GitActionPushApproved, rec.ApprovedBy, "desktop-main", "approved", ""); err != nil {
		_, _, _ = s.store.FailPushApproval(ctx, rec.ID, "audit_failed", err.Error())
		return PushResult{}, err
	}
	state, err := s.inspect(ctx, rec.Repository, rec.Remote)
	if err != nil || state.root != rec.Repository || state.remoteURL != rec.RemoteURL || state.branch != rec.Branch || state.head != rec.ExpectedHeadSHA {
		message := "repository, remote, branch, or HEAD changed after approval was requested"
		if err != nil {
			message = err.Error()
		}
		_, _, _ = s.store.FailPushApproval(ctx, rec.ID, "validation_failed", message)
		_ = s.audit(ctx, rec, domain.GitActionPushRejected, rec.ApprovedBy, "git-broker", "rejected", message)
		return PushResult{}, fmt.Errorf("%w: %s", ErrRepositoryRace, message)
	}
	rec, ok, err = s.store.ClaimPushApproval(ctx, rec.ID, now)
	if err != nil {
		return PushResult{}, err
	}
	if !ok {
		return PushResult{}, ErrNotConsumable
	}
	// UNKNOWN is durable before git starts. If the broker process dies during
	// push there is no automatic retry; the audit remains an explicit prompt for
	// the user to inspect the remote before requesting another approval.
	if err := s.audit(ctx, rec, domain.GitActionPushStarted, rec.ApprovedBy, "git-broker", "UNKNOWN", ""); err != nil {
		_, _, _ = s.store.FailPushApproval(ctx, rec.ID, "audit_failed", err.Error())
		return PushResult{}, err
	}
	refspec := rec.ExpectedHeadSHA + ":refs/heads/" + rec.Branch
	out, pushErr := s.pushApproved(ctx, rec, refspec)
	if pushErr != nil {
		message := strings.TrimSpace(out + " " + pushErr.Error())
		_, _, _ = s.store.FailPushApproval(ctx, rec.ID, "push_failed", message)
		_ = s.audit(ctx, rec, domain.GitActionPushFailed, rec.ApprovedBy, "git-broker", "failed", message)
		return PushResult{}, errors.New(message)
	}
	rec, ok, err = s.store.CompletePushApproval(ctx, rec.ID, out)
	if err != nil {
		return PushResult{}, err
	}
	if !ok {
		return PushResult{}, errors.New("push completed but approval result could not be persisted")
	}
	if err := s.audit(ctx, rec, domain.GitActionPushSucceeded, rec.ApprovedBy, "git-broker", "succeeded", ""); err != nil {
		return PushResult{}, err
	}
	return PushResult{ApprovalID: rec.ID, Status: rec.Status, Result: rec.Result}, nil
}

// pushApproved executes the privileged push from a fresh Host-owned bare Git
// directory. The approved repository contributes objects only: its hooks and
// repository-local config are never loaded by the privileged Git process.
func (s *Service) pushApproved(ctx context.Context, rec domain.PushApproval, refspec string) (string, error) {
	commonDir, err := s.git(ctx, rec.Repository, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(rec.Repository, commonDir)
	}
	commonDir, err = filepath.Abs(filepath.Clean(commonDir))
	if err != nil {
		return "", err
	}
	objectDir := filepath.Join(commonDir, "objects")
	if info, statErr := os.Stat(objectDir); statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = errors.New("not a directory")
		}
		return "", fmt.Errorf("git object directory: %w", statErr)
	}

	brokerDir, err := os.MkdirTemp("", "ao-git-broker-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(brokerDir)
	if _, err := s.run.Run(ctx, brokerDir, []string{"init", "--bare", "."}, brokerEnvironment()); err != nil {
		return "", err
	}
	env := append(brokerEnvironment(), "GIT_OBJECT_DIRECTORY="+objectDir)
	return s.run.Run(ctx, brokerDir, []string{"push", "--no-verify", "--", rec.RemoteURL, refspec}, env)
}

func (s *Service) Revoke(ctx context.Context, approvalID, requestedBy string) error {
	rec, ok, err := s.store.GetPushApproval(ctx, approvalID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if _, ok, err = s.store.RevokePushApproval(ctx, approvalID); err != nil {
		return err
	} else if !ok {
		return ErrNotConsumable
	}
	return s.audit(ctx, rec, domain.GitActionPushCancelled, requestedBy, "desktop-main", "cancelled", "")
}

func (s *Service) inspect(ctx context.Context, dir, remote string) (repositoryState, error) {
	if strings.TrimSpace(dir) == "" {
		return repositoryState{}, errors.New("session has no workspace repository")
	}
	root, err := s.git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositoryState{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return repositoryState{}, err
	}
	root = filepath.Clean(root)
	head, err := s.git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return repositoryState{}, err
	}
	branch, err := s.git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return repositoryState{}, errors.New("detached HEAD cannot be pushed")
	}
	if err := s.validateBranch(ctx, root, branch); err != nil {
		return repositoryState{}, errors.New("current branch is not a valid Git branch")
	}
	if err := validateRemoteName(remote); err != nil {
		return repositoryState{}, err
	}
	remoteURL, err := s.git(ctx, root, "remote", "get-url", "--push", "--", remote)
	if err != nil {
		return repositoryState{}, err
	}
	if filepath.IsAbs(remoteURL) && s.allowLocalRemotes {
		// Deterministic test fixture only.
	} else if err := validateRemoteURL(remoteURL); err != nil {
		return repositoryState{}, err
	}
	return repositoryState{root: root, remote: remote, remoteURL: remoteURL, branch: branch, head: head}, nil
}

func (s *Service) validateBranch(ctx context.Context, dir, branch string) error {
	_, err := s.git(ctx, dir, "check-ref-format", "--branch", branch)
	return err
}

func validateRemoteURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") {
		return errors.New("remote URL is empty or contains whitespace")
	}
	lower := strings.ToLower(raw)
	if strings.Contains(raw, "::") || strings.HasPrefix(lower, "ext::") || strings.HasPrefix(lower, "file:") || strings.HasPrefix(lower, "git:") {
		return errors.New("remote transport is not allowed")
	}
	if filepath.IsAbs(raw) {
		return errors.New("local filesystem remotes are not allowed")
	}
	if strings.HasPrefix(raw, "git@") {
		hostPath := strings.TrimPrefix(raw, "git@")
		colon := strings.IndexByte(hostPath, ':')
		if colon <= 0 || colon == len(hostPath)-1 || strings.ContainsAny(hostPath, "\\\r\n\t ") {
			return errors.New("invalid SSH remote URL")
		}
		if !safeRemoteHost(hostPath[:colon]) || !safeRemotePath(hostPath[colon+1:]) {
			return errors.New("SSH remote URL contains unsupported characters")
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return errors.New("remote URL must have a host and no query parameters or fragments")
	}
	if !safeRemoteHost(parsed.Hostname()) || !safeRemotePath(strings.TrimPrefix(parsed.Path, "/")) {
		return errors.New("remote URL contains unsupported host or path characters")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.User != nil {
			return errors.New("HTTPS remote URL must not contain embedded credentials")
		}
	case "ssh":
		if parsed.User == nil || parsed.User.Username() != "git" {
			return errors.New("SSH remote URL must use the git user")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return errors.New("SSH remote URL must not contain a password")
		}
	default:
		return errors.New("remote transport is not allowed")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return errors.New("remote URL must identify a repository")
	}
	return nil
}

func safeRemoteHost(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-')
	}) == -1
}

func safeRemotePath(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~/-", r))
	}) == -1
}

func validateRemoteName(remote string) error {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\r\n\t ") {
		return errors.New("invalid remote name")
	}
	if strings.Contains(remote, "\\") {
		return errors.New("invalid remote name")
	}
	if strings.Contains(remote, "::") {
		return errors.New("invalid remote name")
	}
	if strings.TrimSpace(remote) != remote {
		return errors.New("remote URL must not contain embedded credentials, query parameters, or fragments")
	}
	return nil
}

func (s *Service) git(ctx context.Context, dir string, args ...string) (string, error) {
	return s.run.Run(ctx, dir, args, brokerEnvironment())
}
func (s *Service) lines(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := s.git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), nil
}
func brokerEnvironment() []string {
	values := make(map[string]string, len(os.Environ())+2)
	keys := make(map[string]string, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			canonical := strings.ToUpper(key)
			if brokerForbiddenEnvironment(canonical) {
				continue
			}
			keys[canonical], values[canonical] = key, value
		}
	}
	for key, value := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "Never"} {
		keys[key], values[key] = key, value
	}
	env := make([]string, 0, len(values))
	for canonical, value := range values {
		env = append(env, keys[canonical]+"="+value)
	}
	sort.Strings(env)
	return env
}

func brokerForbiddenEnvironment(key string) bool {
	if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
		return true
	}
	switch key {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_PARAMETERS",
		"GIT_EXEC_PATH", "GIT_TEMPLATE_DIR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT":
		return true
	default:
		return false
	}
}
func (s *Service) audit(ctx context.Context, a domain.PushApproval, action domain.GitAction, requestedBy, executedBy, result, message string) error {
	now := s.now().UTC()
	return s.store.CreateGitActionAudit(ctx, domain.GitActionAudit{ID: uuid.NewString(), ProjectID: a.ProjectID,
		SessionID: a.SessionID, Action: action, Repository: a.Repository, Remote: a.RemoteURL, Branch: a.Branch, HeadSHA: a.ExpectedHeadSHA,
		ApprovalID: a.ID, RequestedBy: requestedBy, ExecutedBy: executedBy, StartedAt: now,
		FinishedAt: &now, Result: result, ErrorMessage: message})
}
