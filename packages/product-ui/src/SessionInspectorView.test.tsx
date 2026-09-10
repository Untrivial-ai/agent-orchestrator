import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  InspectorActivityTimelineView,
  InspectorPullRequestCardView,
  InspectorReviewsView,
  SessionInspectorShellView,
  type InspectorReviewLabels,
} from "./SessionInspectorView";
import type { ExternalLinkProps } from "./external-link";

function ExternalLink({
  ariaLabel,
  children,
  stopPropagation,
  ...props
}: ExternalLinkProps) {
  return (
    <a
      {...props}
      aria-label={ariaLabel}
      onClick={stopPropagation ? (event) => event.stopPropagation() : undefined}
    >
      {children}
    </a>
  );
}

function setRenderedOverflow(
  element: HTMLElement,
  axis: "horizontal" | "vertical",
  overflowing: boolean,
) {
  if (axis === "vertical") {
    Object.defineProperties(element, {
      clientHeight: { configurable: true, value: 64 },
      scrollHeight: { configurable: true, value: overflowing ? 96 : 64 },
    });
  } else {
    Object.defineProperties(element, {
      clientWidth: { configurable: true, value: 240 },
      scrollWidth: { configurable: true, value: overflowing ? 320 : 240 },
    });
  }
  fireEvent(window, new Event("resize"));
}

function expandFirstReviewGroup() {
  const row = screen.getAllByTestId("review-pr-row")[0]!;
  if (row.getAttribute("aria-expanded") === "false") fireEvent.click(row);
  return row;
}

const tabs = [
  { id: "summary" as const, icon: <svg />, label: "Summary" },
  { id: "reviews" as const, icon: <svg />, label: "Reviews" },
  { badge: true, id: "browser" as const, icon: <svg />, label: "Browser" },
  {
    displayLabel: "2 Files",
    id: "files" as const,
    icon: <svg />,
    label: "Files",
  },
];

describe("SessionInspectorShellView", () => {
  it("preserves the tab semantics, responsive labels, badge, and host slots", () => {
    const onViewChange = vi.fn();
    const { rerender } = render(
      <SessionInspectorShellView
        activeView="summary"
        ariaLabel="Session inspector"
        browserPoppedOut={false}
        browserView={<div role="tabpanel">browser slot</div>}
        filesView={<div role="tabpanel">files slot</div>}
        onViewChange={onViewChange}
        reviewsView={<div role="tabpanel">reviews slot</div>}
        summaryView={<div role="tabpanel">summary slot</div>}
        tabs={tabs}
      />,
    );


		expect(screen.getByRole("complementary", { name: "Session inspector" })).toBeInTheDocument();
		expect(screen.getByRole("tablist")).toHaveClass("session-inspector__tablist", "gap-1");
		expect(screen.getByRole("tablist")).not.toHaveClass("gap-2");
		expect(screen.getByRole("tablist").parentElement).toHaveClass("pl-1");
		expect(screen.getByRole("tablist").parentElement).not.toHaveClass("pl-2", "pl-2.5");
		expect(screen.getByRole("tablist").parentElement?.nextElementSibling).toHaveClass(
			"board-scrollbar",
			"overflow-x-hidden",
		);
		expect(screen.getByRole("tab", { name: "Summary" })).toHaveAttribute("aria-selected", "true");
		expect(screen.getByRole("tab", { name: "Summary" })).toHaveClass("shrink-0", "size-control-md", "p-0");
		expect(screen.getByRole("tab", { name: "Summary" })).not.toHaveClass("flex-1");
		expect(screen.getByRole("tab", { name: "Summary" })).not.toHaveClass("min-w-0");
		expect(screen.getByRole("tab", { name: "Summary" })).toHaveAttribute("tabindex", "0");
		expect(screen.getByRole("tab", { name: "Browser" })).toHaveAttribute("tabindex", "-1");
		const filesLabel = within(screen.getByRole("tab", { name: "Files" })).getByText("2 Files");
		expect(filesLabel).toHaveClass("sr-only");
		expect(filesLabel).not.toHaveClass("session-inspector__responsive-label");
		expect(filesLabel).not.toHaveClass("truncate", "min-w-0");
		expect(filesLabel).not.toHaveClass("@max-[350px]/inspector:hidden");
		expect(screen.getByTestId("browser-unseen-indicator")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("tab", { name: "Browser" }));
		expect(onViewChange).toHaveBeenCalledWith("browser");
		fireEvent.keyDown(screen.getByRole("tab", { name: "Summary" }), { key: "ArrowRight" });
		expect(onViewChange).toHaveBeenLastCalledWith("reviews");
		expect(screen.getByRole("tab", { name: "Reviews" })).toHaveFocus();

    rerender(
      <SessionInspectorShellView
        activeView="reviews"
        ariaLabel="Session inspector"
        browserPoppedOut={false}
        onViewChange={onViewChange}
        reviewsView={<div role="tabpanel">reviews slot</div>}
        tabs={tabs}
      />,
    );
    expect(screen.getByText("reviews slot")).toBeInTheDocument();

    rerender(
      <SessionInspectorShellView
        activeView="browser"
        ariaLabel="Session inspector"
        browserPoppedOut={false}
        browserView={<div role="tabpanel">browser slot</div>}
        onViewChange={onViewChange}
        summaryView={<div role="tabpanel">summary slot</div>}
        tabs={tabs}
      />,
    );
    const body = screen.getByRole("tablist").parentElement?.nextElementSibling;
    expect(body).toHaveClass(
      "session-inspector__body--browser",
      "p-0",
      "overflow-hidden",
    );
    expect(body).not.toHaveClass("p-3");
    expect(screen.getByText("browser slot")).toBeInTheDocument();
  });

  it("renders the loading state without tab chrome", () => {
    render(
      <SessionInspectorShellView
        activeView="summary"
        ariaLabel="Session inspector"
        browserPoppedOut={false}
        loadingText="Loading session…"
        onViewChange={vi.fn()}
        tabs={tabs}
      />,
    );
    expect(screen.getByText("Loading session…")).toHaveClass(
      "text-settings-muted",
    );
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
  });
});

describe("portable inspector presentations", () => {
  it("renders PR facts and host-owned actions from a neutral view model", () => {
    render(
      <InspectorPullRequestCardView
        countNounLabel={(count, noun) => `${count} ${noun}s`}
        externalLink={ExternalLink}
        mergeAction={<button type="button">Merge</button>}
        openLabel="Open PR #12"
        pr={{
          additions: 4,
          author: "ada",
          card: {
            primary: {
              key: "merge",
              label: "Ready to merge",
              links: [],
              tone: "success",
            },
            supporting: [],
          },
          changedFiles: 2,
          deletions: 1,
          href: "https://example.com/pull/12",
          number: 12,
          provider: "github",
          sourceBranch: "feature",
          state: "open",
          stateLabel: "open",
          targetBranch: "main",
          title: "Portable inspector",
        }}
      />,
    );
    expect(
      screen.getByRole("link", { name: "Portable inspector" }),
    ).toHaveAttribute("href", "https://example.com/pull/12");
    expect(
      screen.getByRole("link", { name: "Open PR #12" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Ready to merge")).toHaveClass("text-success");
    expect(screen.getByRole("button", { name: "Merge" })).toBeInTheDocument();
  });

  it("renders timeline events with current-state marker treatment", () => {
    render(
      <InspectorActivityTimelineView
        events={[
          {
            content: <span>Working</span>,
            markerBreathe: true,
            markerTone: "#60a5fa",
            timestamp: null,
            tone: "now",
          },
          {
            content: <span>Created workspace</span>,
            timestamp: "2h ago",
            tone: "neutral",
          },
        ]}
      />,
    );
    const events = screen.getAllByTestId("inspector-timeline-event");
    expect(events).toHaveLength(2);
    expect(events[0].querySelector(".animate-status-pulse")).toHaveStyle({
      background: "#60a5fa",
    });
    expect(screen.getByText("2h ago")).toHaveClass("font-mono", "text-passive");
  });

  it("owns grouped review disclosure while the host supplies markdown and assets", () => {
    const renderAvatar = vi.fn((harness: string) => (
      <span data-testid="avatar">{harness}</span>
    ));
    const renderMarkdown = vi.fn((body: string) => <p>{body}</p>);
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            ao: {
              notInjected: true,
              runs: [
                {
                  body: "Looks good.",
                  createdAtLabel: "5m ago",
                  harness: "codex",
                  id: "run-1",
                  status: "delivered",
                  url: "https://example.com/review",
                  verdict: { label: "Approved", tone: "success" },
                },
              ],
            },
            github: {
              entries: [
                {
                  body: "Ship it.",
                  id: "github-review-1",
                  isBot: true,
                  reviewerId: "review-bot",
                  submittedAt: "2026-08-09T10:00:00Z",
                  submittedAtLabel: "5m ago",
                  verdict: { label: "Approved", tone: "success" },
                },
              ],
              unresolved: 0,
              unresolvedBy: [],
            },
            meta: "#12 · 5m ago",
            number: 12,
            title: "Portable inspector",
            verdict: { label: "Approved", tone: "success" },
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        renderAvatar={renderAvatar}
        renderMarkdown={renderMarkdown}
      />,
    );

    const row = screen.getByTestId("review-pr-row");
    expect(row).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Looks good.")).not.toBeInTheDocument();
    expect(screen.queryByText("Ship it.")).not.toBeInTheDocument();
    fireEvent.click(row);
    expect(row).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Looks good.")).toBeInTheDocument();
    expect(screen.getByText("Ship it.")).toBeInTheDocument();
    expect(screen.queryByRole("button", {
      name: /review-bot.*Approved/,
    })).not.toBeInTheDocument();
    const githubSummary = screen
      .getByText("Ship it.")
      .closest('[data-testid="github-review-summary"]');
    expect(githubSummary).toBeInTheDocument();
    expect(githubSummary).toHaveClass("select-text");
    expect(screen.queryByText("Not injected")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "View on PR" })).not.toBeInTheDocument();
    expect(renderAvatar).toHaveBeenCalledWith("codex");
    expect(renderMarkdown).toHaveBeenCalledTimes(2);
  });

  it("shows the newest GitHub review when history prepends", () => {
    const olderBody = "Older review";
    const newerBody = "Newer review";
    const olderReview = {
      body: olderBody,
      id: "github-review-old",
      reviewerId: "ada",
      submittedAt: "2026-08-09T10:00:00Z",
      submittedAtLabel: "10m ago",
      verdict: { label: "Changes requested", tone: "danger" as const },
    };
    const group = (entries: (typeof olderReview)[]) => [
      {
        github: { entries, unresolved: 0, unresolvedBy: [] },
        meta: "#12",
        number: 12,
        title: "Portable inspector",
      },
    ];
    const view = (entries: (typeof olderReview)[]) => (
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={group(entries)}
        isLoading={false}
        labels={reviewLabels}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />
    );
    const { rerender } = render(view([olderReview]));
    expandFirstReviewGroup();
    expect(screen.getByText(olderBody)).toBeInTheDocument();

    rerender(
      view([
        {
          ...olderReview,
          body: newerBody,
          id: "github-review-new",
          reviewerId: "grace",
          submittedAt: "2026-08-09T11:00:00Z",
          submittedAtLabel: "Now",
        },
        olderReview,
      ]),
    );

    const cards = screen.getAllByTestId("github-review-card");
    expect(within(cards[0]).getByText("grace")).toBeInTheDocument();
    expect(within(cards[0]).getByText(newerBody)).toBeInTheDocument();
  });

  it("keeps external review actions in the header without toggling nested comments", () => {
    const onRequestRereview = vi.fn();
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: "Please take another pass after fixes land.",
                  canRequestRereview: true,
                  id: "github-review-1",
                  pullRequestUrl: "https://example.com/pull/12",
                  reviewerId: "maya",
                  resolvedComments: [{ body: "Already addressed.", resolved: true }],
                  reviewUrl: "https://example.com/review",
                  submittedAt: "2026-08-09T10:00:00Z",
                  submittedAtLabel: "1h ago",
                  verdict: { label: "Changes requested", tone: "danger" },
                },
              ],
              unresolved: 0,
              unresolvedBy: [],
            },
            meta: "#12",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        onRequestRereview={onRequestRereview}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    expect(screen.getByText("Please take another pass after fixes land.")).toBeInTheDocument();
    const reviewToggle = screen.getByRole("button", { name: /maya.*Changes requested/i });
    const actionMenu = screen.getByRole("button", { name: "Review actions" });
    const externalReview = screen.getByTestId("github-review-card");
    expect(screen.getByTestId("external-review-header")).toContainElement(actionMenu);
    expect(reviewToggle).not.toContainElement(actionMenu);
    expect(within(externalReview).queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    expect(reviewToggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(actionMenu);
    expect(screen.getByRole("link", { name: "Open in System Browser" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Request to re-review PR" }));
    expect(onRequestRereview).toHaveBeenCalledWith(
      expect.objectContaining({ id: "github-review-1", reviewerId: "maya" }),
    );
    expect(reviewToggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Resolved comments · 1")).not.toBeInTheDocument();
    fireEvent.click(reviewToggle);
    expect(reviewToggle).toHaveAttribute("aria-expanded", "true");
    expect(within(externalReview).queryByRole("button", { name: "Show less" })).not.toBeInTheDocument();
    expect(screen.getByText("Resolved comments · 1")).toBeInTheDocument();
  });

  it("does not offer re-review for an approved external review", () => {
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: "Looks good.",
                  id: "github-review-approved",
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                  submittedAt: "2026-08-09T10:00:00Z",
                  submittedAtLabel: "1h ago",
                  verdict: { label: "Approved", tone: "success" },
                },
              ],
              unresolved: 0,
              unresolvedBy: [],
            },
            meta: "#12",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        onRequestRereview={vi.fn()}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Request to re-review PR" }),
    ).not.toBeInTheDocument();
  });

  it("shows unresolved inline review comments inside each reviewer dropdown", async () => {
    const onResolveInlineComment = vi.fn().mockResolvedValue(undefined);
    const onSendInlineComment = vi.fn().mockResolvedValue(undefined);
    const onViewInlineCommentInFile = vi.fn();
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: "Please address the inline notes before merge.",
                  id: "github-review-1",
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                  submittedAt: "2026-08-09T10:00:00Z",
                  submittedAtLabel: "1h ago",
                  verdict: { label: "Commented", tone: "neutral" },
                  inlineComments: [
                    {
                      body: "This branch leaks the resize listener on unmount.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 42,
                      url: "https://example.com/comment",
                    },
                    {
                      body: "This was sent to the worker already.",
                      autoInjectReview: true,
                      url: "https://example.com/comment-sent",
                    },
                  ],
                },
              ],
              unresolved: 2,
              unresolvedBy: [
                {
                  count: 2,
                  links: [
                    {
                      body: "This branch leaks the resize listener on unmount.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 42,
                      url: "https://example.com/comment",
                    },
                    {
                      body: "This was sent to the worker already.",
                      autoInjectReview: true,
                      url: "https://example.com/comment-sent",
                    },
                  ],
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                },
              ],
            },
            meta: "#12 · 2 unresolved",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        onResolveInlineComment={onResolveInlineComment}
        onSendInlineComment={onSendInlineComment}
        onViewInlineCommentInFile={onViewInlineCommentInFile}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expect(
      screen.queryByTestId("github-inline-comments"),
    ).not.toBeInTheDocument();
    expandFirstReviewGroup();
    expect(
      screen.getByRole("button", { name: /maya.*Commented/i }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.queryByText("This branch leaks the resize listener on unmount."),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    expect(screen.getByTestId("github-inline-comments")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open comments · 2" })).toBeInTheDocument();
    expect(screen.getByText("#12 · 2 unresolved")).toBeInTheDocument();
    expect(screen.getByText("src/panel.tsx:42")).toBeInTheDocument();
    expect(
      screen.getByText("This branch leaks the resize listener on unmount."),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Send to worker agent" }),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Comment actions" })[0]!);
    fireEvent.click(
      screen.getByRole("button", { name: "Send to worker agent" }),
    );
    expect(onSendInlineComment).toHaveBeenCalledWith(
      expect.objectContaining({
        body: "This branch leaks the resize listener on unmount.",
        file: "src/panel.tsx",
        line: 42,
        reviewerId: "maya",
        url: "https://example.com/comment",
      }),
    );
    await waitFor(() => {
      const sentStatuses = screen.getAllByRole("status", {
        name: "Sent to worker agent",
      });
      expect(sentStatuses).toHaveLength(2);
      expect(sentStatuses[0]).toHaveClass("text-success");
      expect(sentStatuses[0]).toHaveAttribute("title", "Sent to worker agent");
    });
    fireEvent.click(screen.getAllByRole("button", { name: "Comment actions" })[0]!);
    fireEvent.click(screen.getByRole("button", { name: "Resolve comment" }));
    expect(onResolveInlineComment).toHaveBeenCalledWith(
      expect.objectContaining({
        body: "This branch leaks the resize listener on unmount.",
        file: "src/panel.tsx",
        line: 42,
        reviewerId: "maya",
        url: "https://example.com/comment",
      }),
    );
    await waitFor(() => expect(screen.getByText("Resolved")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "View in file" }));
    expect(onViewInlineCommentInFile).toHaveBeenCalledWith(
      expect.objectContaining({ file: "src/panel.tsx", line: 42 }),
    );
    expect(screen.getByRole("link", { name: "Open on GitHub" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy comment link" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy file path" })).not.toBeInTheDocument();
  });

  it("surfaces inline review comment send failures", async () => {
    const onSendInlineComment = vi
      .fn()
      .mockRejectedValue(new Error("send failed"));
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: undefined,
                  id: "unresolved-maya",
                  inlineComments: [
                    {
                      body: "Please tighten this spacing.",
                      autoInjectReview: false,
                      url: "https://example.com/comment",
                    },
                  ],
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                  submittedAt: "",
                  submittedAtLabel: "",
                  verdict: { label: "Commented", tone: "neutral" },
                },
              ],
              unresolved: 1,
              unresolvedBy: [
                {
                  count: 1,
                  links: [
                    {
                      body: "Please tighten this spacing.",
                      autoInjectReview: false,
                      url: "https://example.com/comment",
                    },
                  ],
                  reviewerId: "maya",
                },
              ],
            },
            meta: "#12 · 1 unresolved",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        onSendInlineComment={onSendInlineComment}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    fireEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    fireEvent.click(screen.getByRole("button", { name: "Comment actions" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Send to worker agent" }),
    );

    await waitFor(() =>
      expect(screen.getByText("Unable to send. Retry.")).toHaveClass(
        "text-error",
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "Comment actions" }));
    expect(
      screen.getByRole("button", { name: "Send to worker agent" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("status", { name: "Sent to worker agent" }),
    ).not.toBeInTheDocument();
  });

  it("keeps resolved comments collapsed until requested", () => {
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: undefined,
                  id: "review-maya",
                  inlineComments: [],
                  resolvedComments: [
                    {
                      body: "This resolved note stays hidden by default.",
                      file: "src/panel.tsx",
                      line: 8,
                      resolved: true,
                      url: "https://example.com/comment",
                    },
                  ],
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                  submittedAt: "",
                  submittedAtLabel: "",
                  verdict: { label: "Commented", tone: "neutral" },
                },
              ],
              unresolved: 0,
              unresolvedBy: [],
            },
            meta: "#12",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    fireEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    expect(screen.getByText("Resolved comments · 1")).toBeInTheDocument();
    expect(
      screen.queryByText("This resolved note stays hidden by default."),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Resolved comments · 1" }));
    expect(
      screen.getByText("This resolved note stays hidden by default."),
    ).toBeInTheDocument();
  });

  it("tracks URL-less inline comments independently when they share a review URL", async () => {
    const onSendInlineComment = vi.fn().mockResolvedValue(undefined);
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[
          {
            github: {
              entries: [
                {
                  body: undefined,
                  id: "unresolved-maya",
                  inlineComments: [
                    {
                      body: "Fix the first comment.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 10,
                    },
                    {
                      body: "Fix the second comment.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 20,
                    },
                  ],
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                  submittedAt: "",
                  submittedAtLabel: "",
                  verdict: { label: "Commented", tone: "neutral" },
                },
              ],
              unresolved: 2,
              unresolvedBy: [
                {
                  count: 2,
                  links: [
                    {
                      body: "Fix the first comment.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 10,
                    },
                    {
                      body: "Fix the second comment.",
                      autoInjectReview: false,
                      file: "src/panel.tsx",
                      line: 20,
                    },
                  ],
                  reviewerId: "maya",
                  reviewUrl: "https://example.com/review",
                },
              ],
            },
            meta: "#12 · 2 unresolved",
            number: 12,
            title: "Portable inspector",
          },
        ]}
        isLoading={false}
        labels={reviewLabels}
        onSendInlineComment={onSendInlineComment}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    fireEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    const actionButtons = screen.getAllByRole("button", { name: "Comment actions" });
    fireEvent.click(actionButtons[0]!);
    fireEvent.click(screen.getByRole("button", { name: "Send to worker agent" }));

    await waitFor(() =>
      expect(
        screen.getByRole("status", { name: "Sent to worker agent" }),
      ).toBeInTheDocument(),
    );
    fireEvent.click(actionButtons[1]!);
    expect(
      screen.getAllByRole("button", { name: "Send to worker agent" }),
    ).toHaveLength(1);
    expect(onSendInlineComment).toHaveBeenCalledTimes(1);
    expect(onSendInlineComment).toHaveBeenCalledWith(
      expect.objectContaining({
        body: "Fix the first comment.",
        file: "src/panel.tsx",
        line: 10,
        reviewerId: "maya",
        url: "https://example.com/review",
      }),
    );
  });

  it("shows summary expansion only when the rendered four-line clamp overflows", async () => {
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[{
          ao: {
            runs: [{
              body: "A rendered Markdown summary whose fit depends on the inspector width.",
              createdAtLabel: "Now",
              harness: "codex",
              id: "run-overflow",
              status: "complete",
              verdict: { label: "Approved", tone: "success" },
            }],
          },
          meta: "#12",
          number: 12,
          title: "Measured overflow",
        }]}
        isLoading={false}
        labels={reviewLabels}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    const summary = screen.getByTestId("review-run-summary");
    setRenderedOverflow(summary, "vertical", false);
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();

    setRenderedOverflow(summary, "vertical", true);
    expect(await screen.findByRole("button", { name: "Show more" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(summary).not.toHaveClass("line-clamp-4");
    fireEvent.click(screen.getByRole("button", { name: "Show less" }));
    expect(summary).toHaveClass("line-clamp-4");

    setRenderedOverflow(summary, "vertical", false);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument());
  });

  it("makes an inline comment expandable only when its preview is multiline or rendered-truncated", async () => {
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[{
          github: {
            entries: [{
              id: "review-comments",
              inlineComments: [{ body: "A short comment", file: "src/panel.tsx", line: 42 }],
              reviewerId: "maya",
              submittedAt: "2026-08-09T10:00:00Z",
              submittedAtLabel: "Now",
              verdict: { label: "Commented", tone: "neutral" },
            }],
            unresolved: 1,
            unresolvedBy: [],
          },
          meta: "#12",
          number: 12,
          title: "Measured comment overflow",
        }]}
        isLoading={false}
        labels={reviewLabels}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    fireEvent.click(screen.getByRole("button", { name: /maya.*Commented/i }));
    const preview = screen.getByText("A short comment");
    setRenderedOverflow(preview, "horizontal", false);
    expect(preview.closest('[role="button"]')).toBeNull();

    setRenderedOverflow(preview, "horizontal", true);
    await waitFor(() => expect(preview.closest('[role="button"]')).toHaveAttribute("aria-expanded", "false"));
    fireEvent.click(preview);
    expect(preview.closest('[role="button"]')).toHaveAttribute("aria-expanded", "true");
  });

  it("offers AO browser, system browser, and worker-send actions for summaries", async () => {
    const onOpenInAOBrowser = vi.fn();
    const onSendReviewSummary = vi.fn().mockResolvedValue(undefined);
    render(
      <InspectorReviewsView
        externalLink={ExternalLink}
        groups={[{
          ao: {
            runs: [{
              body: "Please update the validation and tests.",
              createdAtLabel: "Now",
              harness: "codex",
              id: "run-actions",
              status: "complete",
              url: "https://github.com/example/repo/pull/12#pullrequestreview-1",
              verdict: { label: "Changes requested", tone: "danger" },
            }],
          },
          meta: "#12",
          number: 12,
          title: "Summary actions",
        }]}
        isLoading={false}
        labels={reviewLabels}
        onOpenInAOBrowser={onOpenInAOBrowser}
        onSendReviewSummary={onSendReviewSummary}
        renderAvatar={() => null}
        renderMarkdown={(body) => <p>{body}</p>}
      />,
    );

    expandFirstReviewGroup();
    const reviewActions = screen.getByRole("button", { name: "Review actions" });
    expect(reviewActions).toHaveClass("border");
    fireEvent.click(reviewActions);
    const sendAction = screen.getByRole("button", { name: "Send to worker agent" });
    expect(sendAction.parentElement?.firstElementChild).toBe(sendAction);
    expect(screen.getByRole("link", { name: "Open in System Browser" })).toBeInTheDocument();
    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("link", { name: "Open in System Browser" })).not.toBeInTheDocument();
    fireEvent.click(reviewActions);
    fireEvent.click(screen.getByRole("button", { name: "Open in AO Browser" }));
    expect(onOpenInAOBrowser).toHaveBeenCalledWith("https://github.com/example/repo/pull/12#pullrequestreview-1");
    expect(screen.getByRole("link", { name: "Open in System Browser" })).toHaveAttribute(
      "href",
      "https://github.com/example/repo/pull/12#pullrequestreview-1",
    );
    fireEvent.click(screen.getByRole("button", { name: "Send to worker agent" }));
    await waitFor(() => expect(onSendReviewSummary).toHaveBeenCalledWith(expect.objectContaining({
      body: "Please update the validation and tests.",
      reviewerId: "codex",
      source: "agent",
    })));
    expect(await screen.findByRole("button", { name: "Sent to worker agent" })).toBeDisabled();
  });
});

const reviewLabels: InspectorReviewLabels = {
  aoSource: "AO",
  bot: "Bot",
  earlierPass: "Earlier pass",
  githubSource: "On GitHub",
  loadingReviews: "Loading reviews",
  loadMoreReviews: (count) => `Load ${count} more`,
  noPastReviewSummaries: "No summaries",
  notInjected: "Not injected",
  openComments: "Open comments",
  openInAOBrowser: "Open in AO Browser",
  openInSystemBrowser: "Open in System Browser",
  openInlineComments: (count) => `${count} open comments`,
  requestRereviewPR: "Request to re-review PR",
  reviewActions: "Review actions",
  reviews: "Reviews",
  reviewedAt: (time) => `Reviewed ${time}`,
  resolvedComments: (count) => `Resolved comments · ${count}`,
  rereviewRequested: "Asked for re-review",
  rereviewRequestFailed: "Unable to request re-review",
  resolveComment: "Resolve comment",
  resolvedReview: "Resolved",
  resolveReviewFailed: "Unable to resolve. Retry.",
  sendToWorkerAgent: "Send to worker agent",
  sentToWorkerAgent: "Sent to worker agent",
  sendToWorkerAgentError: "Unable to send. Retry.",
  workerAgentWorkingOnFeedback: "Worker agent is working on this feedback.",
  showLatestReviewOnly: "Show latest only",
  showLess: "Show less",
  showMore: "Show more",
  commentNumber: (number) => `Comment ${number}`,
  unresolvedCount: (count) => `${count} unresolved`,
  viewInFile: "View in file",
  viewInFileWorkInProgress: "View in file is coming soon.",
  viewOnPR: "View on PR",
};
