export type ItemAction = {
	id: string;
	label: string;
	/** SF Symbol for the iOS menu row. */
	systemImage?: string;
	destructive?: boolean;
	onPress: () => void;
};

export type ItemActionsMenuProps = {
	actions: readonly ItemAction[];
	accessibilityLabel: string;
	disabled?: boolean;
	loading?: boolean;
};
