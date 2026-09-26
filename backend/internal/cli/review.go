package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	reviewSubmitRetryWindow   = 30 * time.Second
	reviewSubmitRetryInterval = 250 * time.Millisecond
)

// reviewRun mirrors the daemon's domain.ReviewRun for the CLI client.
type reviewRun struct {
	ID             string     `json:"id"`
	ReviewID       string     `json:"reviewId"`
	SessionID      string     `json:"sessionId"`
	BatchID        string     `json:"batchId"`
	Harness        string     `json:"harness"`
	PRURL          string     `json:"prUrl"`
	TargetSHA      string     `json:"targetSha"`
	Status         string     `json:"status"`
	Verdict        string     `json:"verdict"`
	Body           string     `json:"body"`
	GithubReviewID string     `json:"githubReviewId"`
	PublishState   string     `json:"publishState"`
	PublishError   string     `json:"publishError,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
}

type reviewState struct {
	PRURL       string     `json:"prUrl"`
	PRNumber    int        `json:"prNumber"`
	Title       string     `json:"title"`
	TargetSHA   string     `json:"targetSha"`
	Status      string     `json:"status"`
	LatestRun   *reviewRun `json:"latestRun,omitempty"`
	PreviousRun *reviewRun `json:"previousRun,omitempty"`
}

type listReviewsResponse struct {
	ReviewerHandleID string        `json:"reviewerHandleId"`
	Reviews          []reviewState `json:"reviews"`
}

// triggerReviewResponse mirrors controllers.TriggerReviewResponse. Only the
// Created flag is needed here, to report whether a new pass was started.
type triggerReviewResponse struct {
	Created bool `json:"created"`
}

// reviewRunResponse mirrors controllers.ReviewRunResponse.
type reviewRunResponse struct {
	Review           reviewRun   `json:"review"`
	Reviews          []reviewRun `json:"reviews"`
	ReviewerHandleID string      `json:"reviewerHandleId"`
}

// submitReviewComment is one inline finding in a submit request.
type submitReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// submitReviewRequest mirrors controllers.SubmitReviewInput.
type submitReviewRequest struct {
	RunID    string                `json:"runId,omitempty"`
	Verdict  string                `json:"verdict,omitempty"`
	Body     string                `json:"body,omitempty"`
	Comments []submitReviewComment `json:"comments,omitempty"`
}

type reviewSubmitOptions struct {
	session      string
	runID        string
	verdict      string
	body         string
	reviewID     string
	reviews      string
	commentPaths []string
	commentLines []int
	commentBodys []string
}

type reviewSessionOptions struct {
	session string
}

type reviewListOptions struct {
	json bool
}

func newReviewCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Manage AO code reviews of a worker's PR",
	}
	cmd.AddCommand(newReviewListCommand(ctx))
	cmd.AddCommand(newReviewSubmitCommand(ctx))
	cmd.AddCommand(newReviewCancelCommand(ctx))
	cmd.AddCommand(newReviewTriggerCommand(ctx))
	return cmd
}

func newReviewListCommand(ctx *commandContext) *cobra.Command {
	var opts reviewListOptions
	cmd := &cobra.Command{
		Use:     "ls <worker-session-id>",
		Aliases: []string{"list"},
		Short:   "List reviews for a worker session",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := strings.TrimSpace(args[0])
			if session == "" {
				return usageError{errors.New("worker session id must not be blank")}
			}
			var res listReviewsResponse
			path := "sessions/" + url.PathEscape(session) + "/reviews"
			if err := ctx.getJSON(cmd.Context(), path, &res); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			return writeReviewList(cmd, session, res)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output reviews as JSON")
	return cmd
}

func newReviewSubmitCommand(ctx *commandContext) *cobra.Command {
	var opts reviewSubmitOptions
	cmd := &cobra.Command{
		Use:   "submit [worker-session-id]",
		Short: "Record a reviewer's result for a worker's PR and publish it to GitHub",
		Long: "Record a reviewer's result for a worker's PR and publish it to GitHub.\n" +
			"Pipe the full Markdown review body from stdin (--body -); AO publishes the\n" +
			"GitHub review (summary plus inline findings) and records the returned id, so\n" +
			"reviewers never post to GitHub themselves. Repeating an identical submission\n" +
			"is safe: the recorded result is returned without republishing.",
		Args: atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.submitReview(cmd, args, opts)
		},
	}
	// Reviewer agents routinely spell flags with underscores (--review_id) rather
	// than hyphens (--review-id); normalize so both resolve to the same flag.
	cmd.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		return pflag.NormalizedName(strings.ReplaceAll(name, "_", "-"))
	})
	cmd.Flags().StringVar(&opts.session, "session", "", "Worker session id (or pass it as the positional argument)")
	cmd.Flags().StringVar(&opts.runID, "run", "", "Review run id (required)")
	cmd.Flags().StringVar(&opts.verdict, "verdict", "", "Review verdict: approved or changes_requested (required)")
	cmd.Flags().StringVar(&opts.body, "body", "", "Review body: - to read the Markdown from stdin (so nothing is written into the worktree)")
	cmd.Flags().StringArrayVar(&opts.commentPaths, "comment-path", nil, "Inline finding path; repeat together with --comment-line and --comment-body, one occurrence group per finding")
	cmd.Flags().IntSliceVar(&opts.commentLines, "comment-line", nil, "Inline finding line; repeat together with --comment-path and --comment-body")
	cmd.Flags().StringArrayVar(&opts.commentBodys, "comment-body", nil, "Single-line inline finding body; repeat together with --comment-path and --comment-line")
	// Legacy inputs from the pre-#5701 contract, kept only to fail with a
	// message that names the replacement instead of "unknown flag".
	cmd.Flags().StringVar(&opts.reviews, "reviews", "", "Removed: submit one review per run with --run, --verdict, --body, and the --comment-* flags")
	_ = cmd.Flags().MarkHidden("reviews")
	cmd.Flags().StringVar(&opts.reviewID, "review-id", "", "Removed: GitHub review ids are outputs; AO publishes the review and records the id")
	_ = cmd.Flags().MarkHidden("review-id")
	return cmd
}

func (c *commandContext) submitReview(cmd *cobra.Command, args []string, opts reviewSubmitOptions) error {
	session := strings.TrimSpace(opts.session)
	if len(args) == 1 {
		session = strings.TrimSpace(args[0])
	}
	if session == "" {
		return usageError{errors.New("usage: worker session id is required (positional or --session)")}
	}
	if strings.TrimSpace(opts.reviews) != "" {
		return usageError{errors.New("--reviews was removed: submit one review per run with --run, --verdict, --body, and the --comment-* flags")}
	}
	if strings.TrimSpace(opts.reviewID) != "" {
		return usageError{errors.New("--review-id was removed: GitHub review ids are outputs; AO publishes the review and records the id")}
	}
	runID := strings.TrimSpace(opts.runID)
	if runID == "" {
		return usageError{errors.New("usage: --run is required")}
	}
	verdict := strings.TrimSpace(opts.verdict)
	if verdict == "" {
		return usageError{errors.New("usage: --verdict is required (approved or changes_requested)")}
	}
	var body string
	switch bodyArg := strings.TrimSpace(opts.body); bodyArg {
	case "":
	case "-":
		// Read the review from stdin so the reviewer never has to write a file
		// into its checkout (where it could be committed onto the worker branch).
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return usageError{fmt.Errorf("read review body: %w", err)}
		}
		body = string(raw)
	default:
		return usageError{errors.New("--body only accepts - (stdin): pipe the review Markdown from stdin instead of passing a file path")}
	}
	findings, err := pairReviewFindings(opts.commentPaths, opts.commentLines, opts.commentBodys)
	if err != nil {
		return err
	}
	if verdict == "changes_requested" && strings.TrimSpace(body) == "" {
		return usageError{errors.New("a changes_requested review requires a body: pipe the Markdown from stdin with --body -")}
	}
	path := "sessions/" + url.PathEscape(session) + "/reviews/submit"
	var res reviewRunResponse
	if err := c.postReviewJSON(cmd.Context(), path, submitReviewRequest{RunID: runID, Verdict: verdict, Body: body, Comments: findings}, &res); err != nil {
		return err
	}
	// A submit response always carries the recorded run's ID and verdict; a
	// structurally valid but half-populated result is a broken contract, not
	// a success to print.
	if strings.TrimSpace(res.Review.ID) == "" || strings.TrimSpace(res.Review.Verdict) == "" {
		return fmt.Errorf("daemon returned empty review result for %s", session)
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "recorded %s review for %s\n", res.Review.Verdict, session); err != nil {
		return err
	}
	// The publication result rides on the same invocation. Nothing here fails
	// the CLI: the review result itself is recorded; only the daemon-side
	// GitHub publication can report a problem.
	switch res.Review.PublishState {
	case "", "published":
		_, err = fmt.Fprintf(out, "published GitHub review %s\n", strings.TrimSpace(res.Review.GithubReviewID))
	case "failed":
		msg := strings.TrimSpace(res.Review.PublishError)
		if msg == "" {
			msg = "provider rejected the review"
		}
		_, err = fmt.Fprintf(out, "GitHub publication failed: %s\nThe review result is recorded; rerun the same command to retry publication\n", msg)
	case "uncertain", "pending", "publishing":
		reason := strings.TrimSpace(res.Review.PublishError)
		if reason == "" {
			reason = "interrupted mid-publish"
		}
		_, err = fmt.Fprintf(out, "GitHub publication outcome unknown (%s); check the pull request before resubmitting\n", reason)
	default:
		_, err = fmt.Fprintf(out, "GitHub publication state %q\n", res.Review.PublishState)
	}
	return err
}

// pairReviewFindings builds the inline findings from the repeatable comment
// flags. Values pair by occurrence: the n-th occurrence of each flag forms one
// finding, and any incomplete group is rejected instead of silently reordered.
// Finding bodies are single-line by contract — multi-line prose belongs in the
// review body, not in shell-quoted flag values.
func pairReviewFindings(paths []string, lines []int, bodys []string) ([]submitReviewComment, error) {
	if len(paths) == 0 && len(lines) == 0 && len(bodys) == 0 {
		return nil, nil
	}
	if len(paths) != len(lines) || len(paths) != len(bodys) {
		return nil, usageError{fmt.Errorf("incomplete inline finding: --comment-path, --comment-line, and --comment-body must occur the same number of times (got %d, %d, %d)", len(paths), len(lines), len(bodys))}
	}
	findings := make([]submitReviewComment, 0, len(paths))
	for i := range paths {
		findPath := strings.TrimSpace(paths[i])
		findBody := bodys[i]
		switch {
		case findPath == "":
			return nil, usageError{fmt.Errorf("inline finding %d: --comment-path must not be blank", i+1)}
		case strings.ContainsAny(findPath, "\n\r"):
			return nil, usageError{fmt.Errorf("inline finding %d: --comment-path must be a single line", i+1)}
		case lines[i] <= 0:
			return nil, usageError{fmt.Errorf("inline finding %d: --comment-line must be a positive line number", i+1)}
		case strings.TrimSpace(findBody) == "":
			return nil, usageError{fmt.Errorf("inline finding %d: --comment-body must not be blank", i+1)}
		case strings.ContainsAny(findBody, "\n\r"):
			return nil, usageError{fmt.Errorf("inline finding %d: --comment-body must be single-line; multi-line prose belongs in the review body", i+1)}
		}
		findings = append(findings, submitReviewComment{Path: findPath, Line: lines[i], Body: findBody})
	}
	return findings, nil
}

// postReviewJSON retries only transport-level daemon unavailability. The
// service accepts an identical completed result idempotently, so replay is safe
// even when a connection drops after the daemon committed the first request.
// Validation/API errors still return immediately and are never retried.
func (c *commandContext) postReviewJSON(ctx context.Context, path string, body submitReviewRequest, out *reviewRunResponse) error {
	retryCtx, cancel := context.WithTimeout(ctx, reviewSubmitRetryWindow)
	defer cancel()

	var lastErr error
	for {
		err := c.postJSON(retryCtx, path, body, out)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, errDaemonUnavailable) {
			return err
		}
		lastErr = err
		if retryCtx.Err() != nil {
			return fmt.Errorf("%w; could not confirm the review result after retrying for %s. It may already be recorded; retry with the same review results", lastErr, reviewSubmitRetryWindow)
		}

		// Deps.Sleep keeps this loop deterministic in unit tests. The short
		// interval bounds cancellation latency without adding a retry goroutine.
		c.deps.Sleep(reviewSubmitRetryInterval)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if retryCtx.Err() != nil {
			return fmt.Errorf("%w; could not confirm the review result after retrying for %s. It may already be recorded; retry with the same review results", lastErr, reviewSubmitRetryWindow)
		}
	}
}

func newReviewCancelCommand(ctx *commandContext) *cobra.Command {
	var opts reviewSessionOptions
	cmd := &cobra.Command{
		Use:     "cancel [worker-session-id]",
		Aliases: []string{"stop"},
		Short:   "Cancel any running review for a worker's PR",
		Args:    atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.stopReview(cmd, args, opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Worker session id (or pass it as the positional argument)")
	return cmd
}

func newReviewTriggerCommand(ctx *commandContext) *cobra.Command {
	var opts reviewSessionOptions
	cmd := &cobra.Command{
		Use:     "trigger [worker-session-id]",
		Aliases: []string{"execute", "restart"},
		Short:   "Trigger a new review pass for a worker's PR",
		Args:    atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.restartReview(cmd, args, opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Worker session id (or pass it as the positional argument)")
	return cmd
}

func (c *commandContext) stopReview(cmd *cobra.Command, args []string, opts reviewSessionOptions) error {
	session := strings.TrimSpace(opts.session)
	if len(args) == 1 {
		session = strings.TrimSpace(args[0])
	}
	if session == "" {
		return usageError{errors.New("usage: worker session id is required (positional or --session)")}
	}
	path := "sessions/" + url.PathEscape(session) + "/reviews/cancel"
	if err := c.postJSON(cmd.Context(), path, struct{}{}, nil); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "cancelled review for %s\n", session)
	return err
}

func (c *commandContext) restartReview(cmd *cobra.Command, args []string, opts reviewSessionOptions) error {
	session := strings.TrimSpace(opts.session)
	if len(args) == 1 {
		session = strings.TrimSpace(args[0])
	}
	if session == "" {
		return usageError{errors.New("usage: worker session id is required (positional or --session)")}
	}
	path := "sessions/" + url.PathEscape(session) + "/reviews/trigger"
	// Decode the response so we can tell whether a new pass was started or an
	// existing run for the same commit was reused, and report it accurately.
	var res triggerReviewResponse
	if err := c.postJSON(cmd.Context(), path, struct{}{}, &res); err != nil {
		return err
	}
	msg := "reused the existing review for %s\n"
	if res.Created {
		msg = "started a new review for %s\n"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), msg, session)
	return err
}

func writeReviewList(cmd *cobra.Command, session string, res listReviewsResponse) error {
	out := cmd.OutOrStdout()
	if len(res.Reviews) == 0 {
		_, err := fmt.Fprintf(out, "No reviews found for %s.\n", session)
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PR\tSTATUS\tVERDICT\tTITLE"); err != nil {
		return err
	}
	for _, review := range res.Reviews {
		verdict := "-"
		if review.LatestRun != nil && review.LatestRun.Verdict != "" {
			verdict = review.LatestRun.Verdict
		}
		if _, err := fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\n", review.PRNumber, review.Status, verdict, review.Title); err != nil {
			return err
		}
	}
	return tw.Flush()
}
