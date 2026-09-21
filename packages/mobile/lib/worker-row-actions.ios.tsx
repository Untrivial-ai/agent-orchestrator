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
import { useThemeState } from "./ThemeProvider";

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

	return (
		<Host style={{ width: ACTION_WIDTH * 2, height: 76 }} colorScheme={scheme}>
			<HStack spacing={16} modifiers={[frame({ width: ACTION_WIDTH * 2, height: 76 })]}>
				<Button
					onPress={() => onSetPinned(!pinned)}
					modifiers={[
						buttonStyle("glass"),
						buttonBorderShape("circle"),
						controlSize("large"),
						labelStyle("iconOnly"),
						tint(pinned ? "#E2AC50" : "#4B87FF"),
						frame({ width: CONTROL_SIZE, height: CONTROL_SIZE }),
						accessibilityLabel(pinned ? `Unpin ${title}` : `Pin ${title}`),
						accessibilityIdentifier("worker-pin"),
					]}
				>
					<Image
						systemName={pinned ? "pin.fill" : "pin"}
						size={19}
						color={pinned ? "#E2AC50" : "#4B87FF"}
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
						tint("#F06A6A"),
						frame({ width: CONTROL_SIZE, height: CONTROL_SIZE }),
						accessibilityLabel(`Delete ${title}`),
						accessibilityIdentifier("worker-delete"),
					]}
				>
					<Image systemName="trash" size={19} color="#F06A6A" />
				</Button>
			</HStack>
		</Host>
	);
}
