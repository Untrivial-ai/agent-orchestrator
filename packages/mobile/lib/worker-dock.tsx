import { Button, Host, TextInput, useNativeState } from "@expo/ui";
import { memo, useEffect } from "react";
import { StyleSheet, View } from "react-native";
import { useTheme, useThemeState } from "./ThemeProvider";
import { workerDockVisibility } from "./worker-dock-layout";
import { space, type } from "./tokens";

export type WorkerDockProps = {
	query: string;
	onQueryChange: (query: string) => void;
	onSpawn: () => void;
	searchOpen: boolean;
	onSearchOpen: () => void;
	onSearchClose: () => void;
	onOpenControls: () => void;
	projectFiltered: boolean;
	projects: readonly { id: string; name: string }[];
	selectedProjectId: string;
	onSelectProject: (projectId: string) => void;
};

export const WorkerDock = memo(function WorkerDock({
	query,
	onQueryChange,
	onSpawn,
	searchOpen,
	onSearchClose,
	onOpenControls,
	projectFiltered,
}: WorkerDockProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const value = useNativeState(query);
	const visibility = workerDockVisibility(searchOpen);

	useEffect(() => {
		if (value.value !== query) value.value = query;
	}, [query, value]);

	return (
		<View style={styles.row}>
			{visibility.showControls ? <Host style={styles.actionHost} colorScheme={scheme} seedColor={t.accent}>
				<Button
					label="Filters"
					onPress={onOpenControls}
					testID="worker-controls"
					variant={projectFiltered ? "filled" : "outlined"}
					style={{ width: 52, height: 52, borderRadius: 28}}
				/>
			</Host> : null}
			{visibility.showSearch ? <Host style={styles.searchHost} colorScheme={scheme} seedColor={t.accent}>
				{
					<TextInput
						value={value}
						onChangeText={onQueryChange}
						placeholder="Search workers"
						autoFocus
						autoCapitalize="none"
						autoCorrect={false}
						returnKeyType="search"
						onBlur={() => {
							if (!query.trim()) onSearchClose();
						}}
						testID="worker-search"
						style={{
							width: "100%",
							height: 52,
							borderRadius: 16,
							backgroundColor: t.bgSubtle,
							borderWidth: StyleSheet.hairlineWidth,
							borderColor: t.borderDefault,
							paddingHorizontal: space.lg,
						}}
						textStyle={{ color: t.textPrimary, fontSize: type.callout.fontSize }}
						placeholderTextColor={t.textTertiary}
					/>
				}
			</Host> : null}
			{visibility.showSpawn ? <Host style={styles.actionHost} colorScheme={scheme} seedColor={t.accent}>
				<Button
					label="+"
					onPress={onSpawn}
					testID="spawn-worker"
					variant="outlined"
					style={{ width: 52, height: 52, borderRadius: 28}}
				/>
			</Host> : null}
		</View>
	);
});

const styles = StyleSheet.create({
	row: { height: 52, flexDirection: "row", gap: space.sm },
	searchHost: { flex: 1, height: 52 },
	actionHost: { width: 52, height: 52 },
});
