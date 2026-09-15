import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentAvatar } from "./AgentAvatar";
import fxLogo from "../assets/agents/fx.svg";

describe("AgentAvatar", () => {
	it("renders the official fx brand asset", () => {
		render(<AgentAvatar provider="fx" />);
		expect(screen.getByRole("img", { name: "fx" })).toHaveAttribute("src", fxLogo);
	});
	it("renders the Prime Agent brand asset", () => {
		render(<AgentAvatar provider="prime-agent" />);

		expect(screen.getByRole("img", { name: "prime-agent" })).toHaveAttribute(
			"src",
			expect.stringContaining("prime-agent.png"),
		);
	});

	it("renders the OMP brand asset", () => {
		render(<AgentAvatar provider="omp" />);

		expect(screen.getByRole("img", { name: "omp" })).toHaveAttribute("src", expect.stringContaining("omp.png"));
	});
});
