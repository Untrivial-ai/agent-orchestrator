package worker

import "time"

// WorkspaceReviewScope selects the two repository states used for a review.
type WorkspaceReviewScope string

const (
	WorkspaceReviewCombined  WorkspaceReviewScope = "combined"
	WorkspaceReviewCommitted WorkspaceReviewScope = "committed"
	WorkspaceReviewStaged    WorkspaceReviewScope = "staged"
	WorkspaceReviewUnstaged  WorkspaceReviewScope = "unstaged"
	WorkspaceReviewUntracked WorkspaceReviewScope = "untracked"
)

type WorkspaceReviewFileStatus string

const (
	WorkspaceReviewUnmodified    WorkspaceReviewFileStatus = "unmodified"
	WorkspaceReviewModified      WorkspaceReviewFileStatus = "modified"
	WorkspaceReviewAdded         WorkspaceReviewFileStatus = "added"
	WorkspaceReviewDeleted       WorkspaceReviewFileStatus = "deleted"
	WorkspaceReviewRenamed       WorkspaceReviewFileStatus = "renamed"
	WorkspaceReviewCopied        WorkspaceReviewFileStatus = "copied"
	WorkspaceReviewUntrackedFile WorkspaceReviewFileStatus = "untracked"
)

type WorkspaceReviewSide string

const (
	WorkspaceReviewBefore WorkspaceReviewSide = "before"
	WorkspaceReviewAfter  WorkspaceReviewSide = "after"
)

type WorkspaceReviewFileSummary struct {
	Path            string                    `json:"path"`
	PreviousPath    string                    `json:"previousPath,omitempty"`
	Status          WorkspaceReviewFileStatus `json:"status"`
	Additions       int                       `json:"additions"`
	Deletions       int                       `json:"deletions"`
	Size            int64                     `json:"size"`
	Binary          bool                      `json:"binary"`
	Editable        bool                      `json:"editable"`
	FileFingerprint string                    `json:"fileFingerprint"`
}

type WorkspaceReviewSections struct {
	Staged    []WorkspaceReviewFileSummary `json:"staged"`
	Unstaged  []WorkspaceReviewFileSummary `json:"unstaged"`
	Untracked []WorkspaceReviewFileSummary `json:"untracked"`
	Committed []WorkspaceReviewFileSummary `json:"committed"`
}

type WorkspaceReviewCommit struct {
	SHA       string                       `json:"sha"`
	Subject   string                       `json:"subject"`
	Author    string                       `json:"author"`
	Timestamp time.Time                    `json:"timestamp"`
	Files     []WorkspaceReviewFileSummary `json:"files"`
}

type WorkspaceReviewSummary struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

type WorkspaceReviewResponse struct {
	WorkspaceVersion string                       `json:"workspaceVersion"`
	CompareBaseSHA   string                       `json:"compareBaseSha,omitempty"`
	CompareBaseRef   string                       `json:"compareBaseRef,omitempty"`
	CompareMode      string                       `json:"compareMode,omitempty"`
	Files            []WorkspaceReviewFileSummary `json:"files"`
	Truncated        bool                         `json:"truncated"`
	Sections         WorkspaceReviewSections      `json:"sections"`
	Commits          []WorkspaceReviewCommit      `json:"commits"`
	Summary          WorkspaceReviewSummary       `json:"summary"`
	Ahead            *int                         `json:"ahead,omitempty"`
	Behind           *int                         `json:"behind,omitempty"`
}

type WorkspaceReviewFileRequest struct {
	Path      string               `json:"path"`
	Scope     WorkspaceReviewScope `json:"scope,omitempty"`
	CommitSHA string               `json:"commitSha,omitempty"`
}

type WorkspaceReviewFileResponse struct {
	WorkspaceReviewFileSummary
	Deleted          bool   `json:"deleted"`
	ImageMediaType   string `json:"imageMediaType,omitempty"`
	Content          string `json:"content"`
	ContentTruncated bool   `json:"contentTruncated"`
	Diff             string `json:"diff"`
	DiffTruncated    bool   `json:"diffTruncated"`
	CompareBaseSHA   string `json:"compareBaseSha,omitempty"`
	CompareBaseRef   string `json:"compareBaseRef,omitempty"`
	CompareMode      string `json:"compareMode,omitempty"`
	WorkspaceVersion string `json:"workspaceVersion"`
	Historical       bool   `json:"historical,omitempty"`
}

type WorkspaceReviewDiffsRequest struct {
	Scope            WorkspaceReviewScope `json:"scope"`
	Paths            []string             `json:"paths"`
	ContextLines     int                  `json:"contextLines"`
	IgnoreWhitespace bool                 `json:"ignoreWhitespace"`
	WorkspaceVersion string               `json:"workspaceVersion,omitempty"`
	CommitSHA        string               `json:"commitSha,omitempty"`
}

type WorkspaceReviewDiffDeferred struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type WorkspaceReviewDiffError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type WorkspaceReviewDiffGroup struct {
	Repository    string                        `json:"repository,omitempty"`
	Patch         string                        `json:"patch"`
	Truncated     bool                          `json:"truncated"`
	IncludedPaths []string                      `json:"includedPaths"`
	Deferred      []WorkspaceReviewDiffDeferred `json:"deferred"`
	Errors        []WorkspaceReviewDiffError    `json:"errors"`
}

type WorkspaceReviewDiffsResponse struct {
	WorkspaceVersion string                     `json:"workspaceVersion"`
	Groups           []WorkspaceReviewDiffGroup `json:"groups"`
}

type WorkspaceReviewRevisionRequest struct {
	Path             string               `json:"path"`
	Scope            WorkspaceReviewScope `json:"scope,omitempty"`
	Side             WorkspaceReviewSide  `json:"side,omitempty"`
	WorkspaceVersion string               `json:"workspaceVersion,omitempty"`
	ExpectedRevision string               `json:"expectedRevision,omitempty"`
	CommitSHA        string               `json:"commitSha,omitempty"`
}

type WorkspaceReviewRevisionResponse struct {
	Path             string              `json:"path"`
	Side             WorkspaceReviewSide `json:"side"`
	Revision         string              `json:"revision,omitempty"`
	WorkspaceVersion string              `json:"workspaceVersion"`
	MediaType        string              `json:"mediaType,omitempty"`
	Encoding         string              `json:"encoding,omitempty"`
	Size             int64               `json:"size"`
	Exists           bool                `json:"exists"`
	Binary           bool                `json:"binary"`
	Truncated        bool                `json:"truncated"`
	Content          string              `json:"content"`
}

type WorkspaceReviewTreeRequest struct {
	Path string `json:"path,omitempty"`
}

type WorkspaceReviewTreeEntry struct {
	Name       string                    `json:"name"`
	Path       string                    `json:"path"`
	Type       string                    `json:"type"`
	Status     WorkspaceReviewFileStatus `json:"status,omitempty"`
	HasChanges bool                      `json:"hasChanges,omitempty"`
	Size       int64                     `json:"size,omitempty"`
	Binary     bool                      `json:"binary,omitempty"`
}

type WorkspaceReviewTreeResponse struct {
	Path      string                     `json:"path"`
	Entries   []WorkspaceReviewTreeEntry `json:"entries"`
	Truncated bool                       `json:"truncated"`
}

type WorkspaceReviewSearchRequest struct {
	Query  string `json:"query"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type WorkspaceReviewSearchResult struct {
	Path            string                    `json:"path"`
	Status          WorkspaceReviewFileStatus `json:"status"`
	Size            int64                     `json:"size"`
	Binary          bool                      `json:"binary"`
	FileFingerprint string                    `json:"fileFingerprint"`
}

type WorkspaceReviewSearchResponse struct {
	Query      string                        `json:"query"`
	Results    []WorkspaceReviewSearchResult `json:"results"`
	NextCursor string                        `json:"nextCursor,omitempty"`
	Truncated  bool                          `json:"truncated"`
}

type WorkspaceReviewWriteRequest struct {
	Path                    string `json:"path"`
	Content                 string `json:"content"`
	ExpectedFileFingerprint string `json:"expectedFileFingerprint"`
}

type WorkspaceReviewWriteResponse struct {
	Path             string `json:"path"`
	Content          string `json:"content"`
	Size             int64  `json:"size"`
	FileFingerprint  string `json:"fileFingerprint"`
	WorkspaceVersion string `json:"workspaceVersion"`
}
