import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { useCoderSessionOptionsStore } from "../stores/coder-session-options-store";
import { AdditionalRepositoriesPicker, CoderTemplatePicker } from "./CoderTemplatePicker";

vi.mock("../hooks/useCoderTemplates", () => ({
	useCoderTemplates: () => ({
		templates: [
			{ id: "template-1", name: "fast", displayName: "Fast workspace", description: "More CPU", parameters: ["size"] },
			{ id: "template-2", name: "lean", displayName: "Lean workspace", description: "Less CPU", parameters: [] },
		],
		isLoading: false,
	}),
}));

beforeEach(() => useCoderSessionOptionsStore.getState().reset());

it("chooses a template from the searchable dropdown", async () => {
	const user = userEvent.setup();
	render(<CoderTemplatePicker orgId="org-1" />);
	await user.click(screen.getByRole("combobox", { name: "Template" }));
	await user.type(screen.getByPlaceholderText("Search templates"), "Fast");
	expect(screen.queryByRole("option", { name: "Lean workspace" })).not.toBeInTheDocument();
	await user.click(screen.getByRole("option", { name: "Fast workspace" }));
	expect(screen.getByRole("combobox", { name: "Template" })).toHaveTextContent("Fast workspace");
	expect(screen.getByText("Machine size")).toBeInTheDocument();
	expect(screen.getByText("Template").parentElement?.parentElement?.parentElement).toContainElement(screen.getByText("Machine size"));
});

it("shows additional repositories as a carousel and moves to a newly added card", async () => {
	useCoderSessionOptionsStore.getState().setExtraRepos([{ url: "https://github.com/acme/one", branch: "main" }]);
	const user = userEvent.setup();
	render(<AdditionalRepositoriesPicker repos={[{ label: "acme/one", url: "https://github.com/acme/one" }]} />);
	await user.click(screen.getByRole("combobox", { name: "Repository 1" }));
	expect(screen.getByRole("listbox", { name: "Repository 1" })).toHaveClass("overflow-y-scroll");
	await user.keyboard("{Escape}");
	act(() => useCoderSessionOptionsStore.getState().setExtraRepos([
		{ url: "https://github.com/acme/one", branch: "main" },
		{ url: "", branch: "" },
	]));
	await waitFor(() => expect(screen.getByText("2 / 2")).toBeInTheDocument());
	expect(screen.getByRole("button", { name: "Next repository" })).toBeDisabled();
	await user.click(screen.getByRole("button", { name: "Previous repository" }));
	expect(screen.getByText("1 / 2")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Add repository" })).not.toBeInTheDocument();
	act(() => useCoderSessionOptionsStore.getState().setExtraRepos([{ url: "https://github.com/acme/one", branch: "main" }]));
	await waitFor(() => expect(screen.queryByRole("button", { name: "Previous repository" })).not.toBeInTheDocument());
	act(() => useCoderSessionOptionsStore.getState().setExtraRepos([]));
	await waitFor(() => expect(screen.queryByText("Additional repositories")).not.toBeInTheDocument());
});
