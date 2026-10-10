package domain

// TestPullRequestSnapshot pins provider metadata and a retained full patch.
// DiffPath is selected by the controller adapter, never by an HTTP caller.
type TestPullRequestSnapshot struct {
	URL               string `json:"url"`
	CheckoutPath      string `json:"checkoutPath"`
	RepositoryURL     string `json:"repositoryUrl"`
	HeadRepositoryURL string `json:"headRepositoryUrl"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	BaseSHA           string `json:"baseSha"`
	HeadSHA           string `json:"headSha"`
	DiffPath          string `json:"diffPath"`
	DiffSHA256        string `json:"diffSha256"`
}
