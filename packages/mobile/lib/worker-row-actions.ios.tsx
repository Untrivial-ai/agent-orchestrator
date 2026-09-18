import { Host } from "@expo/ui";
import { Button, GlassEffectContainer, HStack, Image } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	frame,
	labelStyle,
	rotationEffect,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize, type } from "./tokens";

const ACTION_WIDTH = 64;
const CONTROL_SIZE = 44;

export function WorkerRowActions({
	title,
	pinned,
	onSetPinned,
	onDelete,
}: {
	title: string;
	pinned: boolean;
	onSetPinned(pinned: boolean): void;
	onDelete(): void;
}) {
	const { scheme } = useThemeState();
	const t = useTheme();

	return (
		<Host style={{ width: ACTION_WIDTH * 2, height: 76 }} colorScheme={scheme}>
			{/* One container for both controls: the system renders their glass in a
			    single pass and blends them as they come together. */}
			<GlassEffectContainer spacing={16}>
			<HStack spacing={16} modifiers={[frame({ width: ACTION_WIDTH * 2, height: 76 })]}>
				<Button
					onPress={() => onSetPinned(!pinned)}
					modifiers={[
						buttonStyle("glass"),
						buttonBorderShape("circle"),
						controlSize("large"),
						labelStyle("iconOnly"),
						tint(pinned ? t.amber : t.accent),
						frame({ width: CONTROL_SIZE, height: CONTROL_SIZE }),
						accessibilityLabel(pinned ? `Unpin ${title}` : `Pin ${title}`),
						accessibilityIdentifier("worker-pin"),
					]}
				>
					<Image
						systemName={pinned ? "pin.fill" : "pin"}
						size={iconSize.lg}
						color={pinned ? t.amber : t.accent}
						modifiers={[rotationEffect(28)]}
					/>
				</Button>
				<Button
					onPress={onDelete}
					modifiers={[
						buttonStyle("glass"),
						buttonBorderShape("circle"),
						controlSize("large"),
						labelStyle("iconOnly"),
						tint(t.red),
						frame({ width: CONTROL_SIZE, height: CONTROL_SIZE }),
						accessibilityLabel(`Delete ${title}`),
						accessibilityIdentifier("worker-delete"),
					]}
				>
					<Image systemName="trash" size={iconSize.lg} color={t.red} />
				</Button>
			</HStack>
			</GlassEffectContainer>
		</Host>
	);
}
