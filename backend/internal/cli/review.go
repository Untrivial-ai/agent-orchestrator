package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
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

// submitReviewItem mirrors controllers.SubmitReviewItem.
type submitReviewItem struct {
	RunID          string `json:"runId"`
	Verdict        string `json:"verdict"`
	Body           string `json:"body,omitempty"`
	GithubReviewID string `json:"githubReviewId,omitempty"`
}

// submitReviewRequest mirrors controllers.SubmitReviewInput.
type submitReviewRequest struct {
	RunID          string             `json:"runId,omitempty"`
	Verdict        string             `json:"verdict,omitempty"`
	Body           string             `json:"body,omitempty"`
	GithubReviewID string             `json:"githubReviewId,omitempty"`
	Reviews        []submitReviewItem `json:"reviews,omitempty"`
}

type reviewSubmitOptions struct {
	session  string
	runID    string
	verdict  string
	body     string
	reviewID string
	reviews  string
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
		Short: "Record a reviewer's result for a worker's PR",
		Args:  atMostOneArg,
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
	cmd.Flags().StringVar(&opts.body, "body", "", "Review body: a path to a Markdown file, or - to read from stdin (so nothing is written into the worktree)")
	cmd.Flags().StringVar(&opts.reviewID, "review-id", "", "Id of the GitHub PR review just posted (the .id from the gh api POST that created the review)")
	cmd.Flags().StringVar(&opts.reviews, "reviews", "", "JSON review results array or object: a path, or - to read from stdin")
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
		return c.submitReviewBatch(cmd, session, opts)
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
	if path := strings.TrimSpace(opts.body); path != "" {
		var raw []byte
		var err error
		if path == "-" {
			// Read the review from stdin so the reviewer never has to write a file
			// into its checkout (where it could be committed onto the worker branch).
			raw, err = io.ReadAll(cmd.InOrStdin())
		} else {
			raw, err = os.ReadFile(path)
		}
		if err != nil {
			return usageError{fmt.Errorf("read review body: %w", err)}
		}
		body = string(raw)
	}
	reviewID := strings.TrimSpace(opts.reviewID)
	path := "sessions/" + url.PathEscape(session) + "/reviews/submit"
	var res reviewRunResponse
	if err := c.postReviewJSON(cmd.Context(), path, submitReviewRequest{RunID: runID, Verdict: verdict, Body: body, GithubReviewID: reviewID}, &res); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "recorded %s review for %s\n", res.Review.Verdict, session)
	return err
}

func (c *commandContext) submitReviewBatch(cmd *cobra.Command, session string, opts reviewSubmitOptions) error {
	if strings.TrimSpace(opts.runID) != "" || strings.TrimSpace(opts.verdict) != "" || strings.TrimSpace(opts.body) != "" || strings.TrimSpace(opts.reviewID) != "" {
		return usageError{errors.New("usage: --reviews cannot be combined with --run, --verdict, --body, or --review-id")}
	}
	reviews, err := readReviewItems(cmd, strings.TrimSpace(opts.reviews))
	if err != nil {
		return err
	}
	path := "sessions/" + url.PathEscape(session) + "/reviews/submit"
	var res reviewRunResponse
	if err := c.postReviewJSON(cmd.Context(), path, submitReviewRequest{Reviews: reviews}, &res); err != nil {
		return err
	}
	count := len(res.Reviews)
	if count == 0 {
		count = len(reviews)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "recorded %d review(s) for %s\n", count, session)
	return err
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
			return fmt.Errorf("%w (review result was not recorded after retrying for %s)", lastErr, reviewSubmitRetryWindow)
		}

		// Deps.Sleep keeps this loop deterministic in unit tests. The short
		// interval bounds cancellation latency without adding a retry goroutine.
		c.deps.Sleep(reviewSubmitRetryInterval)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if retryCtx.Err() != nil {
			return fmt.Errorf("%w (review result was not recorded after retrying for %s)", lastErr, reviewSubmitRetryWindow)
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

func readReviewItems(cmd *cobra.Command, path string) ([]submitReviewItem, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, usageError{fmt.Errorf("read review results: %w", err)}
	}
	var req submitReviewRequest
	if err := json.Unmarshal(raw, &req); err == nil && len(req.Reviews) > 0 {
		return req.Reviews, nil
	}
	var reviews []submitReviewItem
	if err := json.Unmarshal(raw, &reviews); err != nil {
		return nil, usageError{fmt.Errorf("decode review results JSON: %w", err)}
	}
	if len(reviews) == 0 {
		return nil, usageError{errors.New("usage: --reviews requires at least one review result")}
	}
	return reviews, nil
}
