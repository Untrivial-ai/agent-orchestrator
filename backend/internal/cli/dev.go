package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/devimport"
)

type devImportProjectsOptions struct {
	fromDataDir string
	dryRun      bool
	json        bool
}

type devImportProjectsRequest struct {
	SourceDataDir string `json:"sourceDataDir"`
	DryRun        bool   `json:"dryRun"`
}

type devImportProjectsResponse struct {
	Report devimport.Report `json:"report"`
}

func newDevCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Developer utilities",
	}
	cmd.AddCommand(newDevImportProjectsCommand(ctx))
	return cmd
}

func newDevImportProjectsCommand(ctx *commandContext) *cobra.Command {
	var opts devImportProjectsOptions
	cmd := &cobra.Command{
		Use:   "import-projects",
		Short: "Copy project registry data into the current AO data dir",
		Long: "Copy active project registry rows from the normal AO data dir into " +
			"the current AO_DATA_DIR. This copies only project metadata, project config, " +
			"and workspace child repo registry; sessions and runtime state are never copied.\n\n" +
			"The target daemon must be running because the import is executed through " +
			"the daemon HTTP API. Use --dry-run to report planned changes without writing.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runDevImportProjects(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.fromDataDir, "from-data-dir", "", "AO data dir to read (default ~/.ao/data)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Report planned changes without writing")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output the import report as JSON")
	return cmd
}

func (c *commandContext) runDevImportProjects(cmd *cobra.Command, opts devImportProjectsOptions) error {
	// Before config.Load and before any path resolution below: both data dirs are
	// expanded, made absolute and symlink-resolved against THIS machine's
	// filesystem, compared here, and then the local source path is POSTed to the
	// daemon as a bare string for it to open on its OWN disk. Every step of that
	// is wrong for a remote target, and the same-dir check would be comparing a
	// local path against a local path to guard a remote import.
	//
	// /api/v1/dev is on the LAN block list today, so this fails loudly anyway —
	// but with ROUTE_NOT_FOUND, which explains none of the above, and the block
	// is not this command's to depend on.
	if err := c.refuseLocalOnly("ao dev import-projects",
		"resolves both data dir paths on the machine running the CLI and then hands that "+
			"local path to the daemon to open on its own disk, where it would name a different "+
			"directory or none at all. Run it on the machine running that daemon"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	sourceDataDir := opts.fromDataDir
	if strings.TrimSpace(sourceDataDir) == "" {
		sourceDataDir, err = defaultNormalDataDir()
		if err != nil {
			return err
		}
	}
	sourceDataDir, err = expandHomePath(sourceDataDir)
	if err != nil {
		return err
	}
	sourceDataDir, err = resolvedPath(sourceDataDir)
	if err != nil {
		return err
	}
	targetDataDir, err := expandHomePath(cfg.DataDir)
	if err != nil {
		return err
	}
	targetDataDir, err = resolvedPath(targetDataDir)
	if err != nil {
		return err
	}

	if sourceDataDir == targetDataDir {
		return usageError{fmt.Errorf("source and target data dirs are the same: %s", sourceDataDir)}
	}

	rep, err := c.executeDevImportProjects(cmd.Context(), sourceDataDir, targetDataDir, opts.dryRun)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	return writeDevImportProjectsSummary(cmd.OutOrStdout(), rep)
}

func (c *commandContext) executeDevImportProjects(ctx context.Context, sourceDataDir, targetDataDir string, dryRun bool) (devimport.Report, error) {
	var resp devImportProjectsResponse
	if err := c.postJSON(ctx, "dev/import-projects", devImportProjectsRequest{
		SourceDataDir: sourceDataDir,
		DryRun:        dryRun,
	}, &resp); err != nil {
		return devimport.Report{}, err
	}
	if resp.Report.TargetDataDir == "" {
		resp.Report.TargetDataDir = targetDataDir
	}
	return resp.Report, nil
}

func writeDevImportProjectsSummary(w io.Writer, rep devimport.Report) error {
	var b strings.Builder
	if rep.DryRun {
		b.WriteString("Dry run -- no changes written.\n")
	}
	fmt.Fprintf(&b, "Source data dir: %s\n", rep.SourceDataDir)
	fmt.Fprintf(&b, "Target data dir: %s\n", rep.TargetDataDir)
	fmt.Fprintf(&b, "Inserted: %d\n", rep.Inserted)
	fmt.Fprintf(&b, "Updated: %d\n", rep.Updated)
	fmt.Fprintf(&b, "Skipped/conflicts: %d\n", rep.Skipped)
	if len(rep.Conflicts) > 0 {
		b.WriteString("\nConflicts:\n")
		for _, conflict := range rep.Conflicts {
			fmt.Fprintf(&b, "  - %s (%s): %s", conflict.ProjectID, conflict.Path, conflict.Reason)
			if conflict.TargetID != "" || conflict.TargetPath != "" {
				fmt.Fprintf(&b, " [target: %s %s]", conflict.TargetID, conflict.TargetPath)
			}
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func defaultNormalDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".ao", "data"), nil
}

func expandHomePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func resolvedPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(abs), nil
	}
	return "", fmt.Errorf("resolve path %s: %w", path, err)
}
