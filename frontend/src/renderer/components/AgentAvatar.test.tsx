import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentAvatar } from "./AgentAvatar";

describe("AgentAvatar", () => {
	it("renders the opencode brand asset", () => {
		render(<AgentAvatar provider="opencode" />);

		expect(screen.getByRole("img", { name: "opencode" })).toHaveAttribute(
			"src",
			expect.stringContaining("opencode.svg"),
		);
	});
});