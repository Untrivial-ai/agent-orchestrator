package project

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var scpStyleGitURL = regexp.MustCompile(`^[^/@:\s]+@[^/:\s]+:(.+)$`)

var allowedCloneSchemes = map[string]struct{}{
	"file":  {},
	"git":   {},
	"http":  {},
	"https": {},
	"ssh":   {},
}

const clonePreparationMarker = ".ao-clone-prepared"

// Clone checks out one remote repository into a user-selected parent folder,
// then registers it through the same Add boundary as an existing local repo.
// The checkout is staged in a unique sibling directory so a failed or cancelled
// clone can never leave a half-populated destination behind.
func (m *Service) Clone(ctx context.Context, in CloneInput) (Project, error) {
	prepared, err := m.prepareClone(ctx, in)
	if err != nil {
		return Project{}, err
	}
	if !repoHasCommit(ctx, prepared.Path) {
		_ = os.RemoveAll(prepared.Path)
		return Project{}, apierr.Invalid("CLONE_EMPTY_REPOSITORY", "AO needs a repository with at least one commit.", nil)
	}

	project, err := m.Add(ctx, AddInput{
		Path:      prepared.Path,
		ProjectID: in.ProjectID,
		Name:      in.Name,
		Config:    in.Config,
	})
	if err != nil {
		_ = os.RemoveAll(prepared.Path)
		return Project{}, err
	}
	return project, nil
}

func (m *Service) PrepareClone(ctx context.Context, in CloneInput) (ClonePreparationResult, error) {
	return m.prepareClone(ctx, in)
}

func (m *Service) CleanupPreparedClone(ctx context.Context, in ClonePreparationCleanupInput) error {
	path, err := normalizePath(in.Path)
	if err != nil {
		return err
	}
	if err := validateRepositorySetupPathSafety(path); err != nil {
		return err
	}
	marker := filepath.Join(path, ".git", clonePreparationMarker)
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return apierr.Invalid("CLONE_CLEANUP_FAILED", "The prepared clone could not be inspected.", map[string]any{"path": path})
	}
	if err := os.RemoveAll(path); err != nil {
		return apierr.Invalid("CLONE_CLEANUP_FAILED", "The abandoned prepared clone could not be removed.", map[string]any{"path": path})
	}
	return nil
}

func (m *Service) prepareClone(ctx context.Context, in CloneInput) (ClonePreparationResult, error) {
	remoteURL := strings.TrimSpace(in.RemoteURL)
	repositoryName, err := cloneRepositoryName(remoteURL)
	if err != nil {
		return ClonePreparationResult{}, err
	}
	parent, err := normalizePath(in.DestinationParent)
	if err != nil {
		return ClonePreparationResult{}, err
	}
	if err := ensureDirectoryPath(parent); err != nil {
		return ClonePreparationResult{}, err
	}
	if in.Config != nil {
		if err := in.Config.Validate(); err != nil {
			return ClonePreparationResult{}, apierr.Invalid("INVALID_PROJECT_CONFIG", err.Error(), nil)
		}
	}
	if in.ProjectID != nil {
		if err := validateProjectID(domain.ProjectID(strings.TrimSpace(*in.ProjectID))); err != nil {
			return ClonePreparationResult{}, err
		}
	}

	target := filepath.Join(parent, repositoryName)
	if err := validateRepositorySetupPathSafety(target); err != nil {
		return ClonePreparationResult{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return ClonePreparationResult{}, apierr.Conflict("CLONE_DESTINATION_EXISTS", "A folder with this repository name already exists in the selected destination.", map[string]any{"path": target})
	} else if !errors.Is(err, os.ErrNotExist) {
		return ClonePreparationResult{}, apierr.Invalid("CLONE_DESTINATION_UNAVAILABLE", "The clone destination could not be inspected.", map[string]any{"path": target})
	}

	temporaryPath, err := os.MkdirTemp(parent, ".ao-clone-")
	if err != nil {
		return ClonePreparationResult{}, apierr.Invalid("CLONE_DESTINATION_UNAVAILABLE", "AO could not prepare the selected clone destination.", nil)
	}
	cleanupPath := temporaryPath
	defer func() {
		if cleanupPath != "" {
			_ = os.RemoveAll(cleanupPath)
		}
	}()

	cmd := aoprocess.CommandContext(ctx, "git", "clone", "--origin", "origin", "--", remoteURL, temporaryPath)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	if err := cmd.Run(); err != nil {
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return ClonePreparationResult{}, apierr.Invalid("GIT_NOT_FOUND", "Git is required to clone a repository.", nil)
		}
		if ctx.Err() != nil {
			return ClonePreparationResult{}, apierr.Invalid("GIT_CLONE_CANCELLED", "Repository cloning was cancelled.", nil)
		}
		return ClonePreparationResult{}, apierr.Invalid("GIT_CLONE_FAILED", "Could not clone this repository. Check the URL, your Git credentials, and your network connection.", nil)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return ClonePreparationResult{}, apierr.Conflict("CLONE_DESTINATION_EXISTS", "The clone destination became unavailable before the repository could be created.", map[string]any{"path": target})
	}
	cleanupPath = target
	if err := os.WriteFile(filepath.Join(target, ".git", clonePreparationMarker), []byte("created by AO clone preparation\n"), 0o600); err != nil {
		return ClonePreparationResult{}, apierr.Invalid("CLONE_PREPARATION_FAILED", "AO could not mark the prepared clone for cleanup.", nil)
	}
	cleanupPath = ""
	return ClonePreparationResult{Path: target, RemoteURL: remoteURL}, nil
}

func removeClonePreparationMarker(path string) {
	_ = os.Remove(filepath.Join(path, ".git", clonePreparationMarker))
}

func cloneRepositoryName(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") || strings.HasPrefix(raw, "-") {
		return "", invalidCloneURL()
	}

	var remotePath string
	if match := scpStyleGitURL.FindStringSubmatch(raw); len(match) == 2 {
		remotePath = match[1]
	} else {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" {
			return "", invalidCloneURL()
		}
		if _, ok := allowedCloneSchemes[strings.ToLower(parsed.Scheme)]; !ok {
			return "", invalidCloneURL()
		}
		scheme := strings.ToLower(parsed.Scheme)
		hasPassword := false
		if parsed.User != nil {
			_, hasPassword = parsed.User.Password()
		}
		if ((scheme == "http" || scheme == "https") && (parsed.User != nil || parsed.RawQuery != "")) || hasPassword {
			return "", apierr.Invalid("GIT_URL_CONTAINS_CREDENTIALS", "Use your configured Git credentials or an SSH URL instead of putting credentials in the repository URL.", nil)
		}
		remotePath = parsed.Path
	}

	remotePath = strings.TrimRight(remotePath, "/\\")
	name := strings.TrimSuffix(filepath.Base(remotePath), ".git")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\<>:"|?*`) {
		return "", invalidCloneURL()
	}
	return name, nil
}

func invalidCloneURL() error {
	return apierr.Invalid("INVALID_GIT_URL", "Enter a valid HTTPS, SSH, Git, or file repository URL.", nil)
}
