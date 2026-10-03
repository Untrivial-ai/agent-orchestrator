import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: apiMocks.get, POST: apiMocks.post },
	apiErrorMessage: vi.fn((_error: unknown, fallback: string) => fallback),
}));

import { checkRequirementsAgain, InstallDependencyDialog, isActiveInstallJob } from "./InstallDependencyDialog";

describe("isActiveInstallJob", () => {
	it.each(["running", "installing", "verifying"])("treats %s as active", (status) => {
		expect(isActiveInstallJob({ target: "codex", status } as never)).toBe(true);
	});

	it.each(["succeeded", "failed", "unsupported", "interrupted"])("treats %s as terminal", (status) => {
		expect(isActiveInstallJob({ target: "codex", status } as never)).toBe(false);
	});
});

describe("checkRequirementsAgain", () => {
	it("forces an agent refresh before refetching startup requirements", async () => {
		const order: string[] = [];
		apiMocks.post.mockImplementation(async () => {
			order.push("refresh");
			return { data: {}, error: undefined };
		});
		const refetch = vi.fn(async () => {
			order.push("refetch");
		});

		await checkRequirementsAgain(refetch);

		expect(apiMocks.post).toHaveBeenCalledWith("/api/v1/agents/refresh");
		expect(refetch).toHaveBeenCalledOnce();
		expect(order).toEqual(["refresh", "refetch"]);
	});
});

describe("InstallDependencyDialog", () => {
	it("uses the OpenCode 2 installer preview notice and legacy install endpoint", async () => {
		apiMocks.get.mockImplementation(async (path: string) => {
			if (path === "/api/v1/system/install/{target}") {
				return {
					data: { target: "opencode-v2", status: "idle", notice: "The daemon says this replaces OpenCode 1." },
					error: undefined,
				};
			}
			return { data: undefined, error: undefined };
		});
		apiMocks.post.mockResolvedValue({ data: { target: "opencode-v2", status: "installing" }, error: undefined });

		render(
			<InstallDependencyDialog
				requirements={[{ id: "harness", label: "Agent harness", required: true, satisfied: false }] as never}
				onRefetchRequirements={vi.fn()}
			/>,
		);

		await userEvent.click(screen.getByRole("radio", { name: /OpenCode 2/i }));

		expect(await screen.findByText("The daemon says this replaces OpenCode 1.")).toHaveAttribute("role", "status");
		await userEvent.click(screen.getByRole("button", { name: "Install selected" }));

		expect(apiMocks.post).toHaveBeenCalledWith("/api/v1/system/install/{target}", {
			params: { path: { target: "opencode-v2" } },
		});
	});

	it("surfaces a failed Check again refresh", async () => {
		apiMocks.post.mockResolvedValue({ data: undefined, error: { message: "daemon unavailable" } });
		const refetch = vi.fn();

		render(<InstallDependencyDialog requirements={[]} onRefetchRequirements={refetch} />);
		await userEvent.click(screen.getByRole("button", { name: "Check again" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("Could not refresh agent inventory.");
		expect(refetch).not.toHaveBeenCalled();
	});
});
