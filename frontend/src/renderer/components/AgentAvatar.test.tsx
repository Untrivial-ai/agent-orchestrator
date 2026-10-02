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

	it("renders the Gemini CLI brand asset", () => {
		render(<AgentAvatar provider="gemini" />);

		const img = screen.getByRole("img", { name: "gemini" });
		expect(img).toHaveAttribute("src", expect.stringContaining("data:image/svg+xml"));
		expect(img).toHaveAttribute("src", expect.stringContaining("Gemini"));
	});

	it("renders the DeepSeek Harness brand asset", () => {
		render(<AgentAvatar provider="deepseek-harness" />);

		const img = screen.getByRole("img", { name: "deepseek-harness" });
		expect(img).toHaveAttribute("src", expect.stringContaining("data:image/svg+xml"));
		expect(img).toHaveAttribute("src", expect.stringContaining("DeepSeek"));
	});

	it("renders the Unreal Agent brand asset", () => {
		render(<AgentAvatar provider="unreal-agent" />);

		expect(screen.getByRole("img", { name: "unreal-agent" })).toHaveAttribute(
			"src",
			expect.stringContaining("unreal-agent.png"),
		);
	});

	it("renders the MiMo Code brand asset", () => {
		render(<AgentAvatar provider="mimo-code" />);

		expect(screen.getByRole("img", { name: "mimo-code" })).toHaveAttribute(
			"src",
			expect.stringContaining("data:image/svg+xml"),
		);
	});

	it("reuses the OpenCode brand asset for OpenCode 2", () => {
		render(
			<>
				<AgentAvatar provider="opencode" />
				<AgentAvatar provider="opencode-v2" />
			</>,
		);

		expect(screen.getByRole("img", { name: "opencode-v2" })).toHaveAttribute(
			"src",
			screen.getByRole("img", { name: "opencode" }).getAttribute("src"),
		);
	});
});
