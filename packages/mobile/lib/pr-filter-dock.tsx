import { StyleSheet, View } from "react-native";
import type { PRListFilter } from "./prView";
import { Pill } from "./ui";
import type { Theme } from "./theme";
import { useThemedStyles } from "./ThemeProvider";

export function PRFilterDock({ filter, counts, onChange }: {
	filter: PRListFilter;
	counts: Record<PRListFilter, number>;
	onChange: (filter: PRListFilter) => void;
}) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.shell}>
			{(["open", "merged", "all"] as PRListFilter[]).map((item) => (
				<Pill
					key={item}
					label={`${item[0].toUpperCase() + item.slice(1)} ${counts[item]}`}
					active={filter === item}
					onPress={() => onChange(item)}
				/>
			))}
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	shell: {
		flexDirection: "row",
		alignItems: "center",
		gap: 4,
		padding: 4,
		borderRadius: 24,
		borderWidth: StyleSheet.hairlineWidth,
		borderColor: t.borderDefault,
		backgroundColor: t.bgElevated,
	},
});
