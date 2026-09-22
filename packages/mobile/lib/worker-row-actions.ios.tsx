import { Host } from "@expo/ui";
import { Button, HStack, Image } from "@expo/ui/swift-ui";
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
			{/* Deliberately not a `GlassEffectContainer`. That is the right wrapper
			    when two glass controls should read as one surface, but inside one the
			    system blends their material as they approach, so pin and delete
			    morphed into a single blob mid-swipe. Each button keeps its own glass
			    here, and the 16pt gap is what separates them. */}
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
		</Host>
	);
}
