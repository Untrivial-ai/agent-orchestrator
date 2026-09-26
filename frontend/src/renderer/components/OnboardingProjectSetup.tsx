import { Cloud, FolderOpen, GitFork } from "lucide-react";
import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { useCloudGate } from "../hooks/useCloudGate";
import { CreateProjectFlow, type PreparedProjectInput } from "./CreateProjectFlow";
import { OnboardingCloudDialog } from "./OnboardingCloudDialog";
import { SetupRow } from "./SetupList";

type ProjectMode = "folder" | "git";

export function OnboardingProjectSetup({
	onPrepared,
	onCloudProjectCreated,
}: {
	onPrepared: (input: PreparedProjectInput | null) => void;
	onCloudProjectCreated: () => void;
}) {
	const { t } = useTranslation();
	const { cloudEnabled } = useCloudGate();
	const [trigger, setTrigger] = useState<{ kind: "clone" | "folder"; nonce: number } | undefined>(undefined);
	const [showCloud, setShowCloud] = useState(false);

	const resetPrepared = useCallback(() => {
		onPrepared(null);
	}, [onPrepared]);

	const startCloud = useCallback(() => {
		resetPrepared();
		setShowCloud(true);
	}, [resetPrepared]);

	// Both rows hand off to the create project flow. It owns the folder picker,
	// the validator, and the git preparation a folder may still need, so nothing
	// here has to decide whether a repository is ready. It reports back through
	// prepareOnly once the repository is.
	const startImport = useCallback(
		(next: ProjectMode) => {
			resetPrepared();
			setTrigger((current) => ({
				kind: next === "folder" ? "folder" : "clone",
				nonce: (current?.nonce ?? 0) + 1,
			}));
		},
		[resetPrepared],
	);

	return (
		<>
			<div className="flex w-full max-w-[520px] flex-col gap-3">
				<SetupRow
					variant="card"
					icon={<GitFork aria-hidden="true" />}
					label={t("createProject.cloneFromGit")}
					description={t("createProject.cloneFromGitDesc")}
					onClick={() => startImport("git")}
				/>
				<SetupRow
					variant="card"
					icon={<FolderOpen aria-hidden="true" />}
					label={t("createProject.openLocal")}
					description={t("createProject.openLocalDesc")}
					onClick={() => startImport("folder")}
				/>
				{cloudEnabled ? (
					<SetupRow
						variant="card"
						icon={<Cloud aria-hidden="true" />}
						label={t("onboarding.createCloudProject")}
						description={t("onboarding.createCloudProjectDetail")}
						onClick={startCloud}
					/>
				) : null}
			</div>
			{cloudEnabled && showCloud ? (
				<OnboardingCloudDialog onClose={() => setShowCloud(false)} onCreated={onCloudProjectCreated} />
			) : null}
			<CreateProjectFlow
				mode="choose"
				variant="onboarding"
				onCloneProject={async () => undefined}
				onCreateProject={async () => undefined}
				onInitializeProject={async (path) => {
					const { error } = await apiClient.POST("/api/v1/projects/initialize", { body: { path } });
					if (error) throw new Error(apiErrorMessage(error));
				}}
				onboardingTrigger={trigger}
				prepareOnly={{ onPrepared }}
			/>
		</>
	);
}
