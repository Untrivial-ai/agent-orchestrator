import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { SettingsDialog } from "./SettingsDialog";

vi.mock("./ProjectSettingsForm", () => ({
	ProjectSettingsForm: ({
		onSaveState,
	}: {
		onSaveState?: (state: ProjectSettingsSaveState) => void;
	}) => (
		<button
			type="button"
			onClick={() =>
				onSaveState?.({
					isPending: true,
					showSaving: false,
					validationError: null,
					mutationError: null,
					saved: false,
					replacementError: null,
				})
			}
		>
			Start pending save
		</button>
	),
}));

vi.mock("./GlobalSettingsForm", () => ({
	GlobalSettingsForm: () => <div>Global settings</div>,
}));

vi.mock("./settings/KeyboardShortcutsSettingsDialog", () => ({
	KeyboardShortcutsSettingsDialog: () => null,
}));

vi.mock("./ConnectMobileModal", () => ({
	ConnectMobileModal: () => null,
}));

describe("SettingsDialog", () => {
	beforeEach(() => {
		useUiStore.setState({ settingsModal: null });
	});

	it("does not dismiss project settings while a save is pending", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		render(<SettingsDialog />);

		await userEvent.click(await screen.findByRole("button", { name: "Start pending save" }));
		const closeButton = screen.getByRole("button", { name: "Close settings" });
		expect(closeButton).toBeDisabled();

		await userEvent.keyboard("{Escape}");
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
	});
});
