export type ProjectKind = "single_repo" | "workspace" | "scratch";

export type ProjectRepositorySummary = {
	name: string;
	relativePath: string;
	repo?: string;
};

export type ProjectSettingsValues = {
	displayName: string;
	defaultBranch: string;
	sessionPrefix: string;
	workerAgent: string;
	orchestratorAgent: string;
	workerModel: string;
	orchestratorModel: string;
	workerMode: string;
	orchestratorMode: string;
	permissions: string;
	reviewerHarness: string;
	intakeEnabled: boolean;
	intakeRepo: string;
	intakeAssignee: string;
};

export type ProjectSettingsValidationCode =
	| "agents_required"
	| "name_required"
	| "intake_assignee_required";

export function validateProjectSettings(
	values: Pick<
		ProjectSettingsValues,
		"displayName" | "workerAgent" | "orchestratorAgent" | "intakeEnabled" | "intakeAssignee"
	>,
	options: { validateIntake?: boolean } = {},
): ProjectSettingsValidationCode | null {
	if (values.workerAgent === "" || values.orchestratorAgent === "") return "agents_required";
	if (values.displayName.trim() === "") return "name_required";
	if (options.validateIntake !== false && values.intakeEnabled && values.intakeAssignee.trim() === "") {
		return "intake_assignee_required";
	}
	return null;
}

export type ProjectSetupSelection = {
	workerAgent: string;
	orchestratorAgent: string;
	intakeEnabled?: boolean;
	intakeAssignee?: string;
};

export function canSubmitProjectSetup(selection: ProjectSetupSelection): boolean {
	return (
		selection.workerAgent !== "" &&
		selection.orchestratorAgent !== "" &&
		(!selection.intakeEnabled || (selection.intakeAssignee?.trim() ?? "") !== "")
	);
}
