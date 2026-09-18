export type OrchestratorRowActionProps = {
	action: "start" | "resume";
	projectName: string;
	busy: boolean;
	onPress: () => void;
};
