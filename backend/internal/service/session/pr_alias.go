package session

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type pullRequestAliasGroup struct {
	primary domain.PullRequest
	aliases []domain.PullRequest
}

func groupPullRequestAliases(prs []domain.PullRequest) []pullRequestAliasGroup {
	groups := make([]pullRequestAliasGroup, 0, len(prs))
	byKey := map[string]int{}
	for _, pr := range prs {
		key := pullRequestAliasKey(pr.URL, pr.Number, pr.SourceBranch, pr.TargetBranch, pr.HeadSHA)
		if key == "" {
			groups = append(groups, pullRequestAliasGroup{primary: pr, aliases: []domain.PullRequest{pr}})
			continue
		}
		if idx, ok := byKey[key]; ok {
			groups[idx].aliases = append(groups[idx].aliases, pr)
			if pr.UpdatedAt.After(groups[idx].primary.UpdatedAt) {
				groups[idx].primary = pr
			}
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, pullRequestAliasGroup{primary: pr, aliases: []domain.PullRequest{pr}})
	}
	return groups
}

func deduplicatePRFacts(prs []domain.PRFacts) []domain.PRFacts {
	out := make([]domain.PRFacts, 0, len(prs))
	byKey := map[string]int{}
	for _, pr := range prs {
		key := pullRequestAliasKey(pr.URL, pr.Number, pr.SourceBranch, pr.TargetBranch, pr.HeadSHA)
		if key == "" {
			out = append(out, pr)
			continue
		}
		if idx, ok := byKey[key]; ok {
			out[idx] = mergePRFacts(out[idx], pr)
			continue
		}
		byKey[key] = len(out)
		out = append(out, pr)
	}
	return out
}

func canonicalizeCurrentHeadReviewRuns(prs []domain.PRFacts, runs []domain.CurrentHeadReviewRun) []domain.CurrentHeadReviewRun {
	if len(prs) == 0 || len(runs) == 0 {
		return append([]domain.CurrentHeadReviewRun(nil), runs...)
	}

	canonicalByURL := make(map[string]string, len(prs))
	mergedByKey := make(map[string]domain.PRFacts, len(prs))
	urlsByKey := make(map[string][]string, len(prs))
	for _, pr := range prs {
		key := pullRequestAliasKey(pr.URL, pr.Number, pr.SourceBranch, pr.TargetBranch, pr.HeadSHA)
		if key == "" {
			canonicalByURL[pr.URL] = pr.URL
			continue
		}
		mergedByKey[key] = mergePRFacts(mergedByKey[key], pr)
		urlsByKey[key] = append(urlsByKey[key], pr.URL)
	}
	for key, merged := range mergedByKey {
		for _, rawURL := range urlsByKey[key] {
			canonicalByURL[rawURL] = merged.URL
		}
	}

	out := make([]domain.CurrentHeadReviewRun, 0, len(runs))
	byCanonicalAndHarness := make(map[string]int, len(runs))
	for _, run := range runs {
		normalized := run
		if canonicalURL, ok := canonicalByURL[run.PRURL]; ok {
			normalized.PRURL = canonicalURL
		}
		key := normalized.PRURL + "\x00" + string(normalized.Harness)
		if idx, ok := byCanonicalAndHarness[key]; ok {
			if currentHeadRunOutranks(normalized, out[idx]) {
				out[idx] = normalized
			}
			continue
		}
		byCanonicalAndHarness[key] = len(out)
		out = append(out, normalized)
	}
	return out
}

func currentHeadRunOutranks(candidate, current domain.CurrentHeadReviewRun) bool {
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

func mergePRFacts(current, next domain.PRFacts) domain.PRFacts {
	if next.UpdatedAt.After(current.UpdatedAt) {
		next.ReviewComments = current.ReviewComments || next.ReviewComments
		next.ExternalApproved = current.ExternalApproved || next.ExternalApproved
		next.ExternalChangesRequested = current.ExternalChangesRequested || next.ExternalChangesRequested
		next.ExternalComments = current.ExternalComments || next.ExternalComments
		if next.SourceBranch == "" {
			next.SourceBranch = current.SourceBranch
		}
		if next.TargetBranch == "" {
			next.TargetBranch = current.TargetBranch
		}
		if next.HeadSHA == "" {
			next.HeadSHA = current.HeadSHA
		}
		return next
	}
	current.ReviewComments = current.ReviewComments || next.ReviewComments
	current.ExternalApproved = current.ExternalApproved || next.ExternalApproved
	current.ExternalChangesRequested = current.ExternalChangesRequested || next.ExternalChangesRequested
	current.ExternalComments = current.ExternalComments || next.ExternalComments
	if current.SourceBranch == "" {
		current.SourceBranch = next.SourceBranch
	}
	if current.TargetBranch == "" {
		current.TargetBranch = next.TargetBranch
	}
	if current.HeadSHA == "" {
		current.HeadSHA = next.HeadSHA
	}
	return current
}

func pullRequestAliasKey(rawURL string, number int, sourceBranch, targetBranch, headSHA string) string {
	host, owner, repoName, parsedNumber, ok := githubPRURLIdentity(rawURL)
	if !ok {
		return ""
	}
	if number != 0 && parsedNumber != 0 && number != parsedNumber {
		return ""
	}
	identityNumber := strconv.Itoa(parsedNumber)
	branch := normalizedAliasPart(sourceBranch)
	head := normalizedAliasPart(headSHA)
	if branch == "" || head == "" {
		return strings.Join([]string{"github", host, owner, repoName, identityNumber}, "|")
	}
	return strings.Join([]string{
		"github-transfer",
		host,
		repoName,
		identityNumber,
		branch,
		normalizedAliasPart(targetBranch),
		head,
	}, "|")
}

func normalizedAliasPart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func githubPRURLIdentity(raw string) (host, owner, repoName string, number int, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", "", "", 0, false
	}
	host = strings.ToLower(u.Host)
	if host != "github.com" && !strings.HasSuffix(host, ".github.com") {
		return "", "", "", 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return "", "", "", 0, false
	}
	if parts[2] != "pull" && parts[2] != "issues" {
		return "", "", "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return "", "", "", 0, false
	}
	return host, strings.ToLower(parts[0]), strings.ToLower(parts[1]), n, true
}
