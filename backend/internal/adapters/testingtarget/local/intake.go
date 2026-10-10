package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

type intakeCommand func(context.Context, string, string, ...string) ([]byte, error)

// PullRequestIntake snapshots GitHub metadata and pinned objects in an AO cache.
// It never checks out a revision or modifies a registered project's Git config.
type PullRequestIntake struct {
	home func() (string, error)
	run  intakeCommand
}

var _ ports.TestingPullRequestIntake = (*PullRequestIntake)(nil)

// NewPullRequestIntake uses gh and Git without creating a target.
func NewPullRequestIntake() *PullRequestIntake {
	return &PullRequestIntake{home: os.UserHomeDir, run: runIntakeCommand}
}

func runIntakeCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := processutil.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	for _, entry := range strippedEnv(os.Environ()) {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	var output, diagnostics bytes.Buffer
	cmd.Stdout = &intakeOutput{buffer: &output, remaining: 32 << 20, limitError: "PR command stdout exceeds 32 MiB"}
	cmd.Stderr = &intakeOutput{buffer: &diagnostics, remaining: 64 << 10, limitError: "PR command stderr exceeds 64 KiB"}
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(diagnostics.String()+output.String()))
	}
	return output.Bytes(), nil
}

func githubPR(raw string) (domain.RepositoryIdentity, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return domain.RepositoryIdentity{}, "", errors.New("use an HTTPS GitHub pull request URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return domain.RepositoryIdentity{}, "", errors.New("expected https://github.com/owner/repo/pull/number")
	}
	n, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || n == 0 {
		return domain.RepositoryIdentity{}, "", errors.New("pull request number must be positive")
	}
	repo, err := domain.ParseRepositoryIdentity("https://github.com/" + parts[0] + "/" + parts[1])
	return repo, parts[3], err
}

func repositoryURL(repo domain.RepositoryIdentity) string {
	return "https://" + repo.Host + "/" + repo.Namespace + "/" + repo.Name
}
func sameRepository(a, b domain.RepositoryIdentity) bool {
	return strings.EqualFold(a.Host, b.Host) && strings.EqualFold(a.Namespace, b.Namespace) && strings.EqualFold(a.Name, b.Name)
}

var pinnedCommit = regexp.MustCompile(`^[a-f0-9]{40}$`)

// Snapshot resolves immutable PR metadata, fetches pinned objects and retains the patch.
func (p *PullRequestIntake) Snapshot(ctx context.Context, project domain.ProjectRecord, prURL string) (snapshot domain.TestPullRequestSnapshot, checkout string, err error) {
	_, number, err := githubPR(prURL)
	if err != nil {
		return snapshot, "", err
	}
	origin := project.RepoOriginURL
	if origin == "" {
		return snapshot, "", errors.New("project has no repository origin")
	}
	allowed := origin
	if project.Config.CanonicalRepoURL != "" {
		allowed = project.Config.CanonicalRepoURL
	}
	allowedRepo, err := domain.ParseRepositoryIdentity(allowed)
	if err != nil || allowedRepo.Host != "github.com" {
		return snapshot, "", errors.New("project must have a GitHub repository")
	}
	// gh resolves the configured repository through provider renames.
	data, err := p.run(ctx, project.Path, "gh", "repo", "view", repositoryURL(allowedRepo), "--json", "url")
	if err != nil {
		return snapshot, "", err
	}
	var canonical struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &canonical); err != nil {
		return snapshot, "", err
	}
	allowedRepo, err = domain.ParseRepositoryIdentity(canonical.URL)
	if err != nil || allowedRepo.Host != "github.com" {
		return snapshot, "", errors.New("provider returned an invalid project repository")
	}
	data, err = p.run(ctx, project.Path, "gh", "pr", "view", prURL, "--json", "url,title,body,baseRefOid,headRefOid,headRepository,headRepositoryOwner")
	if err != nil {
		return snapshot, "", err
	}
	var pr struct {
		URL, Title, Body, BaseRefOid, HeadRefOid string
		HeadRepository                           struct{ Name string }
		HeadRepositoryOwner                      struct{ Login string }
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		return snapshot, "", err
	}
	resolved, resolvedNumber, err := githubPR(pr.URL)
	if err != nil || !sameRepository(resolved, allowedRepo) || resolvedNumber != number {
		return snapshot, "", errors.New("pull request does not belong to the configured project repository")
	}
	if !pinnedCommit.MatchString(pr.BaseRefOid) || !pinnedCommit.MatchString(pr.HeadRefOid) {
		return snapshot, "", errors.New("provider returned an invalid base or head commit SHA")
	}
	headRepo, err := domain.ParseRepositoryIdentity("https://github.com/" + pr.HeadRepositoryOwner.Login + "/" + pr.HeadRepository.Name)
	if err != nil {
		return snapshot, "", errors.New("pull request head repository is unavailable")
	}
	home, err := p.home()
	if err != nil {
		return snapshot, "", err
	}
	key := sha256.Sum256([]byte(origin))
	root := filepath.Join(home, ".ao", "dev", "agentic-target", "repos", hex.EncodeToString(key[:])[:16])
	if err := os.MkdirAll(root, 0o700); err != nil {
		return snapshot, "", err
	}
	if resolved, e := filepath.EvalSymlinks(root); e != nil || resolved != root {
		return snapshot, "", errors.New("PR cache must not contain symlinks")
	}
	lock, err := os.OpenFile(filepath.Join(root, "intake.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return snapshot, "", fmt.Errorf("reserve PR intake: %w", err)
	}
	defer func() { err = errors.Join(err, releaseIntakeLease(lock)) }()
	checkout = filepath.Join(root, "checkout")
	if _, err = os.Lstat(checkout); errors.Is(err, os.ErrNotExist) {
		if _, err = p.run(ctx, root, "git", "clone", "--no-checkout", repositoryURL(allowedRepo), checkout); err != nil {
			return snapshot, "", err
		}
	} else if err != nil {
		return snapshot, "", err
	}
	if resolved, e := filepath.EvalSymlinks(checkout); e != nil || resolved != checkout {
		return snapshot, "", errors.New("PR checkout must not contain symlinks")
	}
	gitInfo, err := os.Lstat(filepath.Join(checkout, ".git"))
	if err != nil || !gitInfo.IsDir() {
		return snapshot, "", errors.New("PR checkout must be a standalone Git repository")
	}
	lease, err := os.OpenFile(filepath.Join(checkout, ".ao-testing-active"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return snapshot, "", fmt.Errorf("target checkout is busy: %w", err)
	}
	defer func() { err = errors.Join(err, releaseIntakeLease(lease)) }()
	cachedOrigin, err := p.run(ctx, checkout, "git", "remote", "get-url", "origin")
	if err != nil {
		return snapshot, "", err
	}
	cachedRepo, err := domain.ParseRepositoryIdentity(strings.TrimSpace(string(cachedOrigin)))
	projectOrigin, originErr := domain.ParseRepositoryIdentity(origin)
	if err != nil || originErr != nil || (!sameRepository(cachedRepo, projectOrigin) && !sameRepository(cachedRepo, allowedRepo)) {
		return snapshot, "", errors.New("warm checkout belongs to a different repository")
	}
	dirty, err := p.run(ctx, checkout, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return snapshot, "", err
	}
	if strings.TrimSpace(string(dirty)) != "" {
		return snapshot, "", errors.New("warm checkout has tracked edits; preserve them before intake")
	}
	for _, pin := range []struct{ repo, sha string }{{repositoryURL(allowedRepo), pr.BaseRefOid}, {repositoryURL(headRepo), pr.HeadRefOid}} {
		if _, err = p.run(ctx, checkout, "git", "fetch", "--no-tags", pin.repo, pin.sha); err != nil {
			return snapshot, "", err
		}
		object, e := p.run(ctx, checkout, "git", "rev-parse", "--verify", pin.sha+"^{commit}")
		if e != nil || strings.TrimSpace(string(object)) != pin.sha {
			return snapshot, "", errors.Join(errors.New("fetched object does not match pinned commit"), e)
		}
	}
	diff, err := p.run(ctx, checkout, "git", "diff", "--no-ext-diff", "--no-textconv", "--binary", pr.BaseRefOid+"..."+pr.HeadRefOid, "--")
	if err != nil {
		return snapshot, "", err
	}
	hash := sha256.Sum256(diff)
	diffHash := hex.EncodeToString(hash[:])
	snapshots := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snapshots, 0o700); err != nil {
		return snapshot, "", err
	}
	if resolved, e := filepath.EvalSymlinks(snapshots); e != nil || resolved != snapshots {
		return snapshot, "", errors.New("PR snapshot directory must not contain symlinks")
	}
	diffPath := filepath.Join(snapshots, diffHash+".diff")
	if err := writeImmutableSnapshot(diffPath, diff); err != nil {
		return snapshot, "", err
	}
	snapshot = domain.TestPullRequestSnapshot{URL: pr.URL, CheckoutPath: checkout, RepositoryURL: repositoryURL(allowedRepo), HeadRepositoryURL: repositoryURL(headRepo), Title: pr.Title, Body: pr.Body, BaseSHA: pr.BaseRefOid, HeadSHA: pr.HeadRefOid, DiffPath: diffPath, DiffSHA256: diffHash}
	metadata, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot, "", err
	}
	metadataHash := sha256.Sum256(metadata)
	if err := writeImmutableSnapshot(filepath.Join(snapshots, hex.EncodeToString(metadataHash[:])+".json"), metadata); err != nil {
		return snapshot, "", err
	}
	return snapshot, checkout, nil
}

func releaseIntakeLease(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return errors.Join(err, file.Close())
	}
	current, err := os.Lstat(file.Name())
	if err != nil || !os.SameFile(info, current) {
		return errors.Join(errors.New("PR intake reservation changed"), err, file.Close())
	}
	return errors.Join(file.Close(), os.Remove(file.Name()))
}
func writeImmutableSnapshot(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("PR snapshot path is not a regular owned file")
		}
		previous, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(previous, data) {
			return errors.Join(errors.New("saved PR snapshot differs from pinned content"), e)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return errors.Join(err, file.Close())
}

// Bound provider/patch output before it can fill daemon memory.
type intakeOutput struct {
	buffer     *bytes.Buffer
	remaining  int
	limitError string
}

func (w *intakeOutput) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, errors.New(w.limitError)
	}
	w.remaining -= len(data)
	return w.buffer.Write(data)
}
