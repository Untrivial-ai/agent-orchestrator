package workerexec

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

var githubPullRequestURL = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+`)

// pullRequestObserver catches PRs opened through tools that bypass the AO
// command wrapper. A PR is adopted only if GitHub's head commit exists in this
// worker's checkout, so a URL merely mentioned by a command is not enough.
type pullRequestObserver struct {
	mu         sync.Mutex
	pending    map[string]struct{}
	textBuffer string
	lookupHead func(context.Context, string) (pullRequestHead, error)
	hasCommit  func(context.Context, string, pullRequestHead) bool
	claim      func(context.Context, string) error
}

type pullRequestHead struct{ SHA, Branch string }

func (o *pullRequestObserver) observe(output Output) {
	activity := output.Activity
	if output.Stream == "stdout" && output.Text != "" {
		o.mu.Lock()
		o.textBuffer += output.Text
		if len(o.textBuffer) > 16<<10 {
			o.textBuffer = o.textBuffer[len(o.textBuffer)-(16<<10):]
		}
		o.mu.Unlock()
	}
	if activity == nil || activity.Kind != "command" || activity.Status != "completed" {
		return
	}
	value, ok := activity.Detail["output"].(string)
	if !ok || value == "" {
		return
	}
	urls := githubPullRequestURL.FindAllString(value, -1)
	if len(urls) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending == nil {
		o.pending = make(map[string]struct{})
	}
	for _, url := range urls {
		o.pending[url] = struct{}{}
	}
}

func (o *pullRequestObserver) claimObserved(ctx context.Context, workspace string) error {
	o.mu.Lock()
	if o.pending == nil {
		o.pending = make(map[string]struct{})
	}
	for _, url := range githubPullRequestURL.FindAllString(o.textBuffer, -1) {
		o.pending[url] = struct{}{}
	}
	o.textBuffer = ""
	urls := make([]string, 0, len(o.pending))
	for url := range o.pending {
		urls = append(urls, url)
	}
	o.mu.Unlock()
	lookup := o.lookupHead
	if lookup == nil {
		lookup = githubPullRequestHead
	}
	has := o.hasCommit
	if has == nil {
		has = checkoutHasCommit
	}
	var failures error
	for _, url := range urls {
		head, err := lookup(ctx, url)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if !has(ctx, workspace, head) {
			o.mu.Lock()
			delete(o.pending, url)
			o.mu.Unlock()
			continue
		}
		if err := o.claim(ctx, url); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		o.mu.Lock()
		delete(o.pending, url)
		o.mu.Unlock()
	}
	return failures
}

func githubPullRequestHead(ctx context.Context, url string) (pullRequestHead, error) {
	output, err := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json", "headRefOid,headRefName").Output()
	if err != nil {
		return pullRequestHead{}, err
	}
	var result struct {
		HeadRefOID  string `json:"headRefOid"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return pullRequestHead{}, err
	}
	if strings.TrimSpace(result.HeadRefOID) == "" || strings.TrimSpace(result.HeadRefName) == "" {
		return pullRequestHead{}, errors.New("GitHub returned no PR head commit or branch")
	}
	return pullRequestHead{SHA: result.HeadRefOID, Branch: result.HeadRefName}, nil
}

func checkoutHasCommit(ctx context.Context, workspace string, head pullRequestHead) bool {
	if !regexp.MustCompile(`^[a-fA-F0-9]{40}$`).MatchString(head.SHA) || strings.HasPrefix(head.Branch, "-") || strings.Contains(head.Branch, "..") {
		return false
	}
	output, err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--verify", "refs/heads/"+head.Branch).Output()
	return err == nil && strings.EqualFold(strings.TrimSpace(string(output)), head.SHA)
}
