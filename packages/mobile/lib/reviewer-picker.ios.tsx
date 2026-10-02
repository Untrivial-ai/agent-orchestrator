import { Host } from "@expo/ui";
import { Button, HStack, Image, Menu, Spacer, Text, VStack } from "@expo/ui/swift-ui";
import { accessibilityIdentifier, buttonStyle, contentShape, font, foregroundStyle, frame, lineLimit, opacity, padding, shapes, tint, truncationMode } from "@expo/ui/swift-ui/modifiers";
import { StyleSheet } from "react-native";
import { haptics } from "./haptics";
import { reviewerLabel, type ReviewerPickerProps } from "./reviewer-picker.types";
import { HarnessImage, useHarnessLogoUris } from "./spawn-composer-controls.ios";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize } from "./tokens";

/** Native pull-down menus for the reviewer and its model, matching the spawn sheet's harness/model menus. */
export function ReviewerPicker({ reviewers, selectedReviewer, effectiveReviewer, onSelectReviewer, models, modelTitle, selectedModel, onSelectModel, busy }: ReviewerPickerProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const logoUris = useHarnessLogoUris(reviewers);
	const shownReviewer = selectedReviewer || effectiveReviewer;
	const reviewerText = selectedReviewer
		? reviewerLabel(reviewers, selectedReviewer)
		: effectiveReviewer ? `Project default · ${reviewerLabel(reviewers, effectiveReviewer)}` : "Project default";
	const modelText = selectedModel ? reviewerLabel(models, selectedModel) : "Provider default";
	const rowHeight = 44;

	return (
		<Host matchContents={{ vertical: true }} style={styles.host} colorScheme={scheme} seedColor={t.accent}>
			<VStack alignment="leading" spacing={0} modifiers={[opacity(busy ? 0.5 : 1)]}>
				<HStack spacing={8} modifiers={[frame({ height: rowHeight }), padding({ horizontal: 2 })]}>
					<Text modifiers={[font({ size: 15, weight: "semibold" }), foregroundStyle(t.textPrimary)]}>Reviewer</Text>
					<Spacer />
					<Menu
						label={
							<HStack spacing={6} modifiers={[frame({ height: rowHeight }), contentShape(shapes.rectangle())]}>
								<HarnessImage uri={logoUris[shownReviewer]} harness={shownReviewer} />
								<Text modifiers={[font({ size: 14, weight: "medium" }), lineLimit(1), truncationMode("tail")]}>{reviewerText}</Text>
								<Image systemName="chevron.up.chevron.down" size={iconSize.xs} />
							</HStack>
						}
						modifiers={[buttonStyle("plain"), tint(t.textSecondary), accessibilityIdentifier("review-reviewer")]}
					>
						<Button
							label="Project default"
							systemImage={selectedReviewer ? "person.2" : "checkmark"}
							onPress={() => { if (busy) return; haptics.select(); onSelectReviewer(""); }}
						/>
						{reviewers.map((agent) => (
							<Button key={agent.id} onPress={() => { if (busy) return; haptics.select(); onSelectReviewer(agent.id); }}>
								<HStack spacing={9}>
									<HarnessImage uri={logoUris[agent.id]} harness={agent.id} />
									<Text>{agent.label}</Text>
									<Spacer />
									{agent.id === selectedReviewer ? <Image systemName="checkmark" size={iconSize.xs} /> : null}
								</HStack>
							</Button>
						))}
					</Menu>
				</HStack>
				{models.length ? (
					<HStack spacing={8} modifiers={[frame({ height: rowHeight }), padding({ horizontal: 2 })]}>
						<Text modifiers={[font({ size: 15, weight: "semibold" }), foregroundStyle(t.textPrimary)]}>{modelTitle}</Text>
						<Spacer />
						<Menu
							label={
								<HStack spacing={5} modifiers={[frame({ height: rowHeight }), contentShape(shapes.rectangle())]}>
									<Text modifiers={[font({ size: 14, weight: "medium" }), lineLimit(1), truncationMode("tail")]}>{modelText}</Text>
									<Image systemName="chevron.up.chevron.down" size={iconSize.xs} />
								</HStack>
							}
							modifiers={[buttonStyle("plain"), tint(t.textSecondary), accessibilityIdentifier("review-model")]}
						>
							<Button
								label="Provider default"
								systemImage={selectedModel ? undefined : "checkmark"}
								onPress={() => { if (busy) return; haptics.select(); onSelectModel(""); }}
							/>
							{models.map((model) => (
								<Button
									key={model.id}
									label={model.label}
									systemImage={model.id === selectedModel ? "checkmark" : undefined}
									onPress={() => { if (busy) return; haptics.select(); onSelectModel(model.id); }}
								/>
							))}
						</Menu>
					</HStack>
				) : null}
			</VStack>
		</Host>
	);
}

const styles = StyleSheet.create({
	host: { width: "100%" },
});
