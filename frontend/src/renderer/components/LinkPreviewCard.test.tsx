import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppLink } from "./AppLink";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock },
	apiErrorMessage: () => "request failed",
}));

// The hook caches per URL in the shared module-level queryClient, so each test
// hovers a distinct URL to stay isolated.
const url = "https://example.com/docs/getting-started";

afterEach(() => {
	getMock.mockReset();
	vi.restoreAllMocks();
});

describe("AppLink link preview", () => {
	it("fetches and shows the preview card on hover", async () => {
		getMock.mockResolvedValue({
			data: {
				url,
				title: "Getting Started",
				description: "How to install and run the thing.",
				siteName: "Example Docs",
				faviconUrl: "https://example.com/favicon.ico",
				imageUrl: "https://example.com/og.png",
			},
			error: undefined,
		});
		render(<AppLink href={url}>docs</AppLink>);

		expect(getMock).not.toHaveBeenCalled();
		fireEvent.pointerOver(screen.getByRole("link"), { pointerType: "mouse" });

		await waitFor(() => expect(screen.getByTestId("link-preview-card")).toBeInTheDocument());
		expect(getMock).toHaveBeenCalledExactlyOnceWith("/api/v1/link-preview", {
			params: { query: { url } },
		});
		expect(screen.getByText("Getting Started")).toBeInTheDocument();
		expect(screen.getByText("How to install and run the thing.")).toBeInTheDocument();
		expect(screen.getByText("Example Docs")).toBeInTheDocument();
		expect(screen.getByText("example.com")).toBeInTheDocument();
		expect(screen.getByTestId("link-preview-card").querySelectorAll("img")).toHaveLength(2);
	});

	it("falls back to the domain when no site name is returned", async () => {
		const fallbackUrl = "https://fallback.example.net/page";
		getMock.mockResolvedValue({ data: { url: fallbackUrl, title: "A page" }, error: undefined });
		render(<AppLink href={fallbackUrl}>docs</AppLink>);

		fireEvent.pointerOver(screen.getByRole("link"), { pointerType: "mouse" });

		await waitFor(() => expect(screen.getByTestId("link-preview-card")).toBeInTheDocument());
		expect(screen.getAllByText("fallback.example.net").length).toBeGreaterThan(0);
		expect(screen.getByText("A page")).toBeInTheDocument();
	});

	it("renders nothing extra when the preview fetch fails", async () => {
		const brokenUrl = "https://broken.example.org/";
		getMock.mockResolvedValue({ data: undefined, error: { message: "upstream down" } });
		render(<AppLink href={brokenUrl}>docs</AppLink>);

		fireEvent.pointerOver(screen.getByRole("link"), { pointerType: "mouse" });

		await waitFor(() => expect(getMock).toHaveBeenCalled());
		await waitFor(() => {
			expect(screen.queryByTestId("link-preview-card")).not.toBeInTheDocument();
			expect(screen.queryByTestId("link-preview-loading")).not.toBeInTheDocument();
		});
	});

	it("does not fetch previews for non-web links", async () => {
		render(<AppLink href="mailto:dev@example.com">mail</AppLink>);

		fireEvent.pointerOver(screen.getByRole("link"), { pointerType: "mouse" });

		await new Promise((resolve) => setTimeout(resolve, 400));
		expect(getMock).not.toHaveBeenCalled();
		expect(screen.queryByTestId("link-preview-card")).not.toBeInTheDocument();
	});

	it("keeps the context menu working while the hover card is wired", async () => {
		const menuUrl = "https://menu.example.io/item";
		getMock.mockResolvedValue({ data: { url: menuUrl }, error: undefined });
		render(<AppLink href={menuUrl}>docs</AppLink>);

		fireEvent.contextMenu(screen.getByRole("link"));

		expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
			"Open in ao browser", "Open in external browser", "Copy link",
		]);
	});
});
