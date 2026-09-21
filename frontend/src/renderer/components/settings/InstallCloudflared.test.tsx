import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { apiClient } from "../../lib/api-client";
import { InstallCloudflared } from "./InstallCloudflared";

vi.mock("../../lib/api-client", () => ({
	apiClient: {
		POST: vi.fn(),
		GET: vi.fn(),
	},
	apiErrorMessage: () => "failed",
}));

beforeEach(() => vi.clearAllMocks());

test("lets a Linux user check again after running the manual command", async () => {
	vi.mocked(apiClient.POST).mockResolvedValue({
		data: {
			target: "cloudflared",
			status: "unsupported",
			command: "sudo apt-get install -y cloudflared",
			error: "Run this command in a terminal.",
		},
		error: undefined,
	} as never);
	const onInstalled = vi.fn();
	render(<InstallCloudflared onInstalled={onInstalled} />);

	await userEvent.click(screen.getByRole("button", { name: /install cloudflared/i }));
	await userEvent.click(await screen.findByRole("button", { name: /check again/i }));

	expect(onInstalled).toHaveBeenCalledOnce();
});
