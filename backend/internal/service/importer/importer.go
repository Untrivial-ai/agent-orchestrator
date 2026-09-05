// Package importer is the controller-facing service for the legacy-AO import.
// It wraps the internal/legacyimport engine with a detection probe (is a legacy
// install present?) and a trigger that runs the import through the live daemon's
// store, so the daemon stays the sole writer. Whether to PROMPT for the import
// is the desktop app's job (the app-state.json migration marker), so this probe
// reports only physical availability, not "already imported".
package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/legacyimport"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Store is the storage slice the import runs through; *sqlite.Store satisfies it.
type Store interface {
	legacyimport.Store
}

// Status reports whether a legacy AO install is physically present to import.
type Status struct {
	Available  bool   `json:"available"`
	LegacyRoot string `json:"legacyRoot"`
}

// Service is the controller-facing import contract.
type Service interface {
	Status(ctx context.Context) (Status, error)
	Run(ctx context.Context) (legacyimport.Report, error)
	Validate(ctx context.Context, in ImportValidationInput) (ImportValidationResult, error)
	PrepareGit(ctx context.Context, in GitPreparationInput) (GitPreparationResult, error)
}

// Import kinds, next steps, Git preparation actions, and event states shared by the import API.
const (
	ImportKindProject   = "project"
	ImportKindWorkspace = "workspace"

	ImportNextStepError            = "error"
	ImportNextStepChooseImportKind = "choose_import_kind"
	ImportNextStepPrepareGit       = "prepare_git"
	ImportNextStepContinue         = "continue"

	GitPreparationActionInit      = "git_init"
	GitPreparationActionCommit    = "git_commit"
	GitPreparationActionSetRemote = "set_remote"

	GitPreparationEventPending = "pending"
	GitPreparationEventRunning = "running"
	GitPreparationEventSuccess = "success"
	GitPreparationEventError   = "error"
)

// ImportValidationInput is the body shape for POST /api/v1/imports/validate.
type ImportValidationInput struct {
	ImportKind string `json:"importKind" enum:"project,workspace" minLength:"1"`
	Path       string `json:"path" minLength:"1"`
}

// GitPreparationInput is the body shape for POST /api/v1/imports/prepare-git.
type GitPreparationInput struct {
	ImportKind       string                          `json:"importKind" enum:"project,workspace" minLength:"1"`
	Path             string                          `json:"path" minLength:"1"`
	ApprovedActions  []string                        `json:"approvedActions,omitempty"`
	RemoteURL        string                          `json:"remoteUrl,omitempty"`
	InitialCommitMsg string                          `json:"initialCommitMessage,omitempty"`
	Repositories     []GitRepositoryPreparationInput `json:"repositories,omitempty"`
}

// GitRepositoryPreparationInput approves Git preparation for one repository.
type GitRepositoryPreparationInput struct {
	RepoPath         string   `json:"repoPath" minLength:"1"`
	ApprovedActions  []string `json:"approvedActions"`
	RemoteURL        string   `json:"remoteUrl,omitempty"`
	InitialCommitMsg string   `json:"initialCommitMessage,omitempty"`
}

// RepoGitStatus describes the Git readiness of one repository candidate.
type RepoGitStatus struct {
	RepoPath        string   `json:"repoPath"`
	IsRepo          bool     `json:"isRepo"`
	HasCommit       bool     `json:"hasCommit"`
	HasOrigin       bool     `json:"hasOrigin"`
	IsEmptyFolder   bool     `json:"isEmptyFolder"`
	NeedsGitInit    bool     `json:"needsGitInit"`
	RequiredActions []string `json:"requiredActions"`
	BlockingErrors  []string `json:"blockingErrors"`
}

// ImportValidationResult is shared by project import validation and future
// workspace import validation.
type ImportValidationResult struct {
	ImportKind     string          `json:"importKind"`
	IsValid        bool            `json:"isValid"`
	BlockingErrors []string        `json:"blockingErrors"`
	Root           RepoGitStatus   `json:"root"`
	ChildRepos     []RepoGitStatus `json:"childRepos,omitempty"`
	// Warning is advisory UI copy for non-blocking classification details.
	Warning  string `json:"warning,omitempty"`
	NextStep string `json:"nextStep" enum:"error,choose_import_kind,prepare_git,continue"`
}

// GitPreparationEvent reports one state transition for a requested Git action.
type GitPreparationEvent struct {
	Action   string `json:"action" enum:"git_init,git_commit,set_remote"`
	RepoPath string `json:"repoPath"`
	State    string `json:"state" enum:"pending,running,success,error"`
	Message  string `json:"message,omitempty"`
	Error    string `json:"error,omitempty"`
}

// GitPreparationResult is the body of POST /api/v1/imports/prepare-git.
type GitPreparationResult struct {
	Events     []GitPreparationEvent  `json:"events"`
	Validation ImportValidationResult `json:"validation"`
}

// Deps bundles the import service's dependencies.
type Deps struct {
	// Store is the rewrite's durable store (the daemon's shared *sqlite.Store).
	Store Store
	// Root overrides the legacy AO root to read. Empty -> the default.
	Root string
}

// Manager implements Service over the daemon's store.
type Manager struct {
	store Store
	root  string
}

var _ Service = (*Manager)(nil)

// New constructs the import service. An empty Root falls back to the default.
func New(deps Deps) *Manager {
	root := deps.Root
	if root == "" {
		root = legacyimport.DefaultLegacyRootDir()
	}
	return &Manager{store: deps.Store, root: root}
}

// Status reports availability only: legacy data present at the root. It never
// errors on a missing legacy store; that is simply "not available".
func (m *Manager) Status(_ context.Context) (Status, error) {
	return Status{Available: legacyimport.HasLegacyData(m.root), LegacyRoot: m.root}, nil
}

// Run executes the import through the daemon's store. Idempotent: the engine
// skips rows that already exist. Legacy files are never modified.
func (m *Manager) Run(ctx context.Context) (legacyimport.Report, error) {
	return legacyimport.Run(ctx, m.store, legacyimport.Options{Root: m.root})
}

// Validate inspects a selected folder for project import readiness without
// mutating Git or the filesystem.
func (m *Manager) Validate(ctx context.Context, in ImportValidationInput) (ImportValidationResult, error) {
	importKind := strings.TrimSpace(in.ImportKind)
	if importKind != ImportKindProject && importKind != ImportKindWorkspace {
		return ImportValidationResult{}, apierr.Invalid("UNSUPPORTED_IMPORT_KIND", "Only project and workspace imports are supported.", map[string]any{"importKind": in.ImportKind})
	}
	path, normalizeErr := normalizeImportPath(in.Path)
	if normalizeErr != nil {
		return invalidImportResult(importKind, strings.TrimSpace(in.Path), "INVALID_PATH"), nil //nolint:nilerr // validation failures are reported in-band so the UI can show blocking errors
	}
	if unsafeImportPath(path) {
		return invalidImportResult(importKind, path, "IMPORT_PATH_UNSAFE"), nil
	}
	result := ImportValidationResult{
		ImportKind:     importKind,
		IsValid:        true,
		BlockingErrors: []string{},
		Root:           RepoGitStatus{RepoPath: path, BlockingErrors: []string{}, RequiredActions: []string{}},
		NextStep:       ImportNextStepContinue,
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return invalidImportResult(importKind, path, "INVALID_PATH"), nil //nolint:nilerr // validation failures are reported in-band so the UI can show blocking errors
	}
	if !info.IsDir() {
		return invalidImportResult(importKind, path, "PATH_NOT_DIRECTORY"), nil
	}
	if isBareImportRepo(ctx, path) {
		return invalidImportResult(importKind, path, "BARE_REPOSITORY"), nil
	}
	if hasUnsupportedImportGitMetadata(path) {
		result.Root.BlockingErrors = append(result.Root.BlockingErrors, "UNSUPPORTED_GIT_METADATA")
		result.BlockingErrors = append(result.BlockingErrors, "UNSUPPORTED_GIT_METADATA")
		result.IsValid = false
		result.NextStep = ImportNextStepError
		return result, nil
	}

	root := inspectImportRepo(ctx, path)
	result.Root = root
	if importKind == ImportKindWorkspace {
		if root.IsRepo {
			result.Warning = "This folder is already a Git project. AO will import it as a project instead of a workspace."
			result.NextStep = ImportNextStepChooseImportKind
			return result, nil
		}
		children, scanErr := directChildImportStatuses(ctx, path)
		if scanErr != nil {
			return invalidImportResult(importKind, path, "CHILD_REPO_SCAN_FAILED"), nil //nolint:nilerr // validation failures are reported in-band so the UI can show blocking errors
		}
		result.ChildRepos = children
		if len(children) == 0 {
			result.Root.BlockingErrors = append(result.Root.BlockingErrors, "WORKSPACE_CHILD_REPO_REQUIRED")
			result.BlockingErrors = append(result.BlockingErrors, "WORKSPACE_CHILD_REPO_REQUIRED")
			result.IsValid = false
			result.NextStep = ImportNextStepError
			return result, nil
		}
		for _, child := range children {
			if len(child.BlockingErrors) > 0 {
				result.BlockingErrors = append(result.BlockingErrors, child.BlockingErrors...)
				result.IsValid = false
				result.NextStep = ImportNextStepError
			}
			if result.NextStep != ImportNextStepError && len(child.RequiredActions) > 0 {
				result.NextStep = ImportNextStepPrepareGit
			}
		}
		return result, nil
	}
	children, scanErr := directChildImportRepos(ctx, path)
	if scanErr != nil {
		return invalidImportResult(importKind, path, "CHILD_REPO_SCAN_FAILED"), nil //nolint:nilerr // validation failures are reported in-band so the UI can show blocking errors
	}
	if len(children) > 0 {
		result.ChildRepos = children
	}
	if !root.IsRepo {
		if len(children) > 0 {
			result.NextStep = ImportNextStepChooseImportKind
			return result, nil
		}
	}
	if root.HasOrigin && len(children) > 0 {
		result.Warning = "Selected folder has direct child repositories, but because the root repository already has an origin remote AO will import it as a project, not a workspace."
	}
	if len(root.RequiredActions) > 0 {
		result.NextStep = ImportNextStepPrepareGit
	}
	return result, nil
}

// PrepareGit executes approved missing Git preparation actions for a project
// import. Actions run in a fixed order and are skipped when already satisfied.
func (m *Manager) PrepareGit(ctx context.Context, in GitPreparationInput) (GitPreparationResult, error) {
	importKind := strings.TrimSpace(in.ImportKind)
	if importKind != ImportKindProject && importKind != ImportKindWorkspace {
		return GitPreparationResult{}, apierr.Invalid("UNSUPPORTED_IMPORT_KIND", "Only project and workspace imports are supported.", map[string]any{"importKind": in.ImportKind})
	}
	validation, err := m.Validate(ctx, ImportValidationInput{ImportKind: importKind, Path: in.Path})
	if err != nil {
		return GitPreparationResult{}, err
	}
	if !validation.IsValid {
		return GitPreparationResult{Validation: validation}, nil
	}
	targets, err := preparationTargets(validation, in)
	if err != nil {
		return GitPreparationResult{}, err
	}
	events := []GitPreparationEvent{}
	for _, target := range targets {
		if unsafeImportPath(target.Status.RepoPath) {
			return GitPreparationResult{}, apierr.Invalid("IMPORT_PATH_UNSAFE", "Selected folder is too broad for automatic Git setup.", map[string]any{"path": target.Status.RepoPath})
		}
		required := actionSet(target.Status.RequiredActions)
		for action := range required {
			if !containsAction(target.Input.ApprovedActions, action) {
				return GitPreparationResult{}, apierr.Invalid("IMPORT_ACTION_APPROVAL_REQUIRED", "Every missing Git preparation action requires explicit approval.", map[string]any{"repoPath": target.Status.RepoPath, "action": action})
			}
		}
		if required[GitPreparationActionSetRemote] && strings.TrimSpace(target.Input.RemoteURL) == "" {
			return GitPreparationResult{}, apierr.Invalid("IMPORT_REMOTE_URL_REQUIRED", "remoteUrl is required before AO can add an origin remote.", map[string]any{"repoPath": target.Status.RepoPath})
		}
		for _, action := range []string{GitPreparationActionInit, GitPreparationActionCommit, GitPreparationActionSetRemote} {
			if !required[action] {
				continue
			}
			events = append(events,
				GitPreparationEvent{RepoPath: target.Status.RepoPath, Action: action, State: GitPreparationEventPending},
				GitPreparationEvent{RepoPath: target.Status.RepoPath, Action: action, State: GitPreparationEventRunning},
			)
			if actionErr := runGitPreparationAction(ctx, target.Status.RepoPath, action, target.Input); actionErr != nil {
				events = append(events, GitPreparationEvent{RepoPath: target.Status.RepoPath, Action: action, State: GitPreparationEventError, Error: actionErr.Error()})
				latest, _ := m.Validate(ctx, ImportValidationInput{ImportKind: importKind, Path: validation.Root.RepoPath})
				return GitPreparationResult{Events: events, Validation: latest}, nil //nolint:nilerr // action failures are reported in-band as progress events for partial recovery
			}
			events = append(events, GitPreparationEvent{RepoPath: target.Status.RepoPath, Action: action, State: GitPreparationEventSuccess})
		}
	}
	latest, err := m.Validate(ctx, ImportValidationInput{ImportKind: importKind, Path: validation.Root.RepoPath})
	if err != nil {
		return GitPreparationResult{}, err
	}
	return GitPreparationResult{Events: events, Validation: latest}, nil
}

func invalidImportResult(importKind, path, code string) ImportValidationResult {
	if importKind == "" {
		importKind = ImportKindProject
	}
	return ImportValidationResult{
		ImportKind:     importKind,
		IsValid:        false,
		BlockingErrors: []string{code},
		Root: RepoGitStatus{
			RepoPath:        path,
			RequiredActions: []string{},
			BlockingErrors:  []string{code},
		},
		NextStep: ImportNextStepError,
	}
}

func inspectImportRepo(ctx context.Context, path string) RepoGitStatus {
	status := RepoGitStatus{RepoPath: path, BlockingErrors: []string{}, RequiredActions: []string{}}
	status.IsEmptyFolder = isImportFolderEmpty(path)
	status.IsRepo = isImportGitRepo(path)
	status.HasCommit = status.IsRepo && importRepoHasCommit(ctx, path)
	status.HasOrigin = status.IsRepo && resolveImportOriginURL(path) != ""
	status.NeedsGitInit = !status.IsRepo
	if status.NeedsGitInit {
		status.RequiredActions = append(status.RequiredActions, GitPreparationActionInit)
	}
	if !status.HasCommit {
		status.RequiredActions = append(status.RequiredActions, GitPreparationActionCommit)
	}
	if !status.HasOrigin {
		status.RequiredActions = append(status.RequiredActions, GitPreparationActionSetRemote)
	}
	return status
}

type gitPreparationTarget struct {
	Status RepoGitStatus
	Input  GitRepositoryPreparationInput
}

func preparationTargets(validation ImportValidationResult, in GitPreparationInput) ([]gitPreparationTarget, error) {
	if validation.ImportKind == ImportKindProject {
		return []gitPreparationTarget{{
			Status: validation.Root,
			Input: GitRepositoryPreparationInput{
				RepoPath:         validation.Root.RepoPath,
				ApprovedActions:  in.ApprovedActions,
				RemoteURL:        in.RemoteURL,
				InitialCommitMsg: in.InitialCommitMsg,
			},
		}}, nil
	}

	byPath := map[string]GitRepositoryPreparationInput{}
	for _, repo := range in.Repositories {
		path, err := normalizeImportPath(repo.RepoPath)
		if err != nil {
			return nil, apierr.Invalid("INVALID_REPOSITORY_PATH", "Repository path is invalid.", map[string]any{"repoPath": repo.RepoPath})
		}
		repo.RepoPath = path
		byPath[path] = repo
	}
	if len(byPath) == 0 {
		return nil, apierr.Invalid("IMPORT_REPOSITORY_APPROVAL_REQUIRED", "Choose at least one repository to prepare.", nil)
	}
	var targets []gitPreparationTarget
	for _, status := range validation.ChildRepos {
		if len(status.RequiredActions) == 0 {
			continue
		}
		input, ok := byPath[status.RepoPath]
		if !ok {
			continue
		}
		targets = append(targets, gitPreparationTarget{Status: status, Input: input})
	}
	if len(targets) != len(byPath) {
		return nil, apierr.Invalid("INVALID_REPOSITORY_PATH", "Each repository must be a direct child that still needs Git setup.", nil)
	}
	return targets, nil
}

func directChildImportRepos(ctx context.Context, root string) ([]RepoGitStatus, error) {
	statuses, err := directChildImportStatuses(ctx, root)
	if err != nil {
		return nil, err
	}
	repos := statuses[:0]
	for _, status := range statuses {
		if status.IsRepo {
			repos = append(repos, status)
		}
	}
	return repos, nil
}

func directChildImportStatuses(ctx context.Context, root string) ([]RepoGitStatus, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var repos []RepoGitStatus
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".git" {
			continue
		}
		child := filepath.Join(root, entry.Name())
		status := inspectImportRepo(ctx, child)
		if isBareImportRepo(ctx, child) {
			status.BlockingErrors = append(status.BlockingErrors, "BARE_REPOSITORY")
		}
		if hasUnsupportedImportGitMetadata(child) {
			status.BlockingErrors = append(status.BlockingErrors, "UNSUPPORTED_GIT_METADATA")
		}
		repos = append(repos, status)
	}
	return repos, nil
}

func runGitPreparationAction(ctx context.Context, path, action string, in GitRepositoryPreparationInput) error {
	switch action {
	case GitPreparationActionInit:
		_, err := importGitOutput(ctx, path, "init", "-b", domain.DefaultBranchName)
		if err != nil {
			return fmt.Errorf("initialize repository: %w", err)
		}
		if _, err := importGitOutput(ctx, path, "config", "--local", gitdefault.ManagedDefaultConfigKey, domain.DefaultBranchName); err != nil {
			return fmt.Errorf("record default branch: %w", err)
		}
	case GitPreparationActionCommit:
		if empty, err := importWorktreeIsEmpty(path); err != nil {
			return fmt.Errorf("inspect files for initial commit: %w", err)
		} else if empty {
			name := filepath.Base(path)
			if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
				return fmt.Errorf("create default README: %w", err)
			}
		}
		if _, err := importGitOutput(ctx, path, "add", "-A"); err != nil {
			return fmt.Errorf("stage files: %w", err)
		}
		msg := strings.TrimSpace(in.InitialCommitMsg)
		if msg == "" {
			msg = "initial commit"
		}
		if _, err := importGitOutput(ctx, path, "-c", "user.name=Agent Orchestrator", "-c", "user.email=ao@example.com", "commit", "--allow-empty", "-m", msg); err != nil {
			return fmt.Errorf("create initial commit: %w", err)
		}
	case GitPreparationActionSetRemote:
		if resolveImportOriginURL(path) != "" {
			return nil
		}
		if _, err := importGitOutput(ctx, path, "remote", "add", "origin", strings.TrimSpace(in.RemoteURL)); err != nil {
			return fmt.Errorf("add origin remote: %w", err)
		}
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	return nil
}

func importWorktreeIsEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return false, nil
		}
	}
	return true, nil
}

func normalizeImportPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("path is required")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if raw == "~" {
			raw = home
		} else {
			raw = filepath.Join(home, raw[2:])
		}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// unsafeImportPath protects broad user and AO-owned directories from the Git
// preparation actions below. Import preparation is deliberately separate from
// project setup, so it cannot rely on the latter's path-safety guard.
func unsafeImportPath(path string) bool {
	clean := comparableImportPath(path)
	if filepath.Dir(clean) == clean {
		return true
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	home = comparableImportPath(home)
	if sameImportPath(clean, home) {
		return true
	}
	for _, broadName := range []string{"Desktop", "Documents", "Downloads"} {
		if sameImportPath(clean, comparableImportPath(filepath.Join(home, broadName))) {
			return true
		}
	}
	aoState := comparableImportPath(filepath.Join(home, ".ao"))
	return sameImportPath(clean, aoState) || isImportDescendant(clean, aoState)
}

func isImportDescendant(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil || rel == "." || rel == "" || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func isImportFolderEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

func isImportGitRepo(path string) bool {
	out, err := aoprocess.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	top := normalizeImportGitPath(path, strings.TrimSpace(string(out)))
	return sameImportPath(top, comparableImportPath(path))
}

func isBareImportRepo(ctx context.Context, path string) bool {
	out, err := importGitOutput(ctx, path, "rev-parse", "--is-bare-repository")
	return err == nil && strings.TrimSpace(out) == "true"
}

func importRepoHasCommit(ctx context.Context, path string) bool {
	_, err := importGitOutput(ctx, path, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func resolveImportOriginURL(path string) string {
	out, err := aoprocess.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hasUnsupportedImportGitMetadata(path string) bool {
	if isImportGitRepo(path) {
		return false
	}
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func normalizeImportGitPath(base, reported string) string {
	if reported == "" {
		return comparableImportPath(reported)
	}
	if !filepath.IsAbs(reported) {
		reported = filepath.Join(base, reported)
	}
	return comparableImportPath(reported)
}

func comparableImportPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	return filepath.Clean(clean)
}

func sameImportPath(a, b string) bool {
	return strings.EqualFold(a, b) || a == b
}

func actionSet(actions []string) map[string]bool {
	out := make(map[string]bool, len(actions))
	for _, action := range actions {
		out[action] = true
	}
	return out
}

func containsAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

func importGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := aoprocess.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
