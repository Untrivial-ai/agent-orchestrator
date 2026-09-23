import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ post: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: apiMocks.post },
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
	it("surfaces a failed Check again refresh", async () => {
		apiMocks.post.mockResolvedValue({ data: undefined, error: { message: "daemon unavailable" } });
		const refetch = vi.fn();

		render(<InstallDependencyDialog requirements={[]} onRefetchRequirements={refetch} />);
		await userEvent.click(screen.getByRole("button", { name: "Check again" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("Could not refresh agent inventory.");
		expect(refetch).not.toHaveBeenCalled();
	});
});
