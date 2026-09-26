import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { buildIntake, IntakeFields, type IntakeForm } from "./IntakeFields";

function IntakeEditor({ variant }: { variant: "default" | "settings" }) {
	const [form, setForm] = useState<IntakeForm>({
		enabled: true,
		provider: "",
		repo: "team/project",
		assignee: "alice",
	});
	return (
		<>
			<IntakeFields variant={variant} form={form} onChange={(patch) => setForm((current) => ({ ...current, ...patch }))} />
			<output aria-label="Saved intake">{JSON.stringify(buildIntake(form))}</output>
		</>
	);
}

describe("intake provider selection", () => {
	it.each(["default", "settings"] as const)("saves OneDev and preserves it when disabling intake in %s controls", async (variant) => {
		render(<IntakeEditor variant={variant} />);
		await userEvent.click(screen.getByRole("button", { name: "Issue tracker" }));
		await userEvent.click(screen.getByRole("menuitem", { name: "OneDev" }));
		expect(screen.getByLabelText("Saved intake")).toHaveTextContent('"provider":"onedev"');
		await userEvent.click(screen.getByRole(variant === "settings" ? "switch" : "checkbox"));
		expect(screen.getByLabelText("Saved intake")).toHaveTextContent('"provider":"onedev"');
		expect(screen.getByLabelText("Saved intake")).not.toHaveTextContent('"enabled":true');
	});

	it("preserves a configured provider when changing the assignee", () => {
		expect(buildIntake({ enabled: true, provider: "onedev", repo: "team/project", assignee: "bob" },
			{ enabled: true, provider: "onedev", repo: "team/project", assignee: "alice" }))
			.toEqual({ enabled: true, provider: "onedev", repo: "team/project", assignee: "bob" });
	});

	it("clears an explicit provider when automatic selection is requested", () => {
		expect(buildIntake({ enabled: true, provider: "", repo: "team/project", assignee: "alice" },
			{ enabled: true, provider: "onedev", repo: "team/project", assignee: "alice" })?.provider)
			.toBeUndefined();
	});
});
