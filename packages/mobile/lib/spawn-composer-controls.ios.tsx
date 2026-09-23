import { Host } from "@expo/ui";
import { Asset } from "expo-asset";
import { Button, Group, HStack, Image, Menu, Spacer, Text, VStack } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	aspectRatio,
	backgroundOverlay,
	buttonStyle,
	clipShape,
	containerRelativeFrame,
	font,
	frame,
	labelStyle,
	opacity,
	padding,
	resizable,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useEffect, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text as RNText, View } from "react-native";
import { chipColorFor } from "./harnessLogo";
import { logoFor } from "./harnessLogoAssets";
import { glassPanel } from "./glass";
import { haptics } from "./haptics";
import type { SpawnComposerControlsProps, SpawnComposerOption } from "./spawn-composer-controls.types";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize, press, space, type } from "./tokens";

export function SpawnComposerControls({
	projects,
	projectId,
	onSelectProject,
	agents,
	harness,
	onSelectHarness,
	models,
	modelSelection,
	modelLabel,
	onSelectModel,
	onAttach,
	onSpawn,
	busy,
	disabled,
}: SpawnComposerControlsProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const logoUris = useHarnessLogoUris(agents);
	const projectLabel = projects.find((project) => project.id === projectId)?.label ?? "Choose project";
	const harnessLabel = agents.find((agent) => agent.id === harness)?.label ?? "Choose harness";

	return (
		<View style={styles.stack}>
			<Host style={styles.controlsHost} colorScheme={scheme} seedColor={t.accent}>
				<VStack alignment="leading" spacing={10} modifiers={[frame({ height: 104, maxWidth: 1000 })]}>
				<Menu
					label={
						<HStack spacing={7}>
							<Image systemName="folder" size={iconSize.sm} />
							<Text modifiers={[font({ size: 14, weight: "medium" })]}>{projectLabel}</Text>
							<Image systemName="chevron.up.chevron.down" size={iconSize.xs} />
						</HStack>
					}
					modifiers={[buttonStyle("plain"), tint(t.textSecondary), padding({ horizontal: 4 }), accessibilityIdentifier("spawn-project")]}
				>
					{projects.map((project) => (
						<Button
							key={project.id}
							label={project.label}
							systemImage={project.id === projectId ? "checkmark" : "folder"}
							onPress={() => { haptics.select(); onSelectProject(project.id); }}
						/>
					))}
				</Menu>

				<HStack
					spacing={6}
					modifiers={[
						padding({ horizontal: 8 }),
						containerRelativeFrame({ axes: "horizontal" }),
						frame({ height: 54 }),
						glassPanel(),
					]}
				>
					<Button
						label="Attach file"
						systemImage="paperclip"
						onPress={() => { haptics.tap(); onAttach(); }}
						modifiers={[
							buttonStyle("plain"),
							labelStyle("iconOnly"),
							frame({ width: 38, height: 38 }),
							tint(t.textPrimary),
							accessibilityIdentifier("spawn-attachment"),
						]}
					/>

					<Menu
						label={
							<HStack spacing={6}>
								<HarnessImage uri={logoUris[harness]} harness={harness} />
								<Text modifiers={[font({ size: 14, weight: "medium" })]}>{harnessLabel}</Text>
								<Image systemName="chevron.down" size={iconSize.xs} />
							</HStack>
						}
						modifiers={[buttonStyle("plain"), tint(t.textPrimary), accessibilityIdentifier("spawn-harness")]}
					>
						{agents.map((agent) => (
							<Button key={agent.id} onPress={() => { haptics.select(); onSelectHarness(agent.id); }}>
								<HStack spacing={9}>
									<HarnessImage uri={logoUris[agent.id]} harness={agent.id} />
									<Text>{agent.label}</Text>
									<Spacer />
									{agent.id === harness ? <Image systemName="checkmark" size={iconSize.xs} /> : null}
								</HStack>
							</Button>
						))}
					</Menu>

					<Menu
						label={
							<HStack spacing={5} modifiers={[frame({ maxWidth: 1000, alignment: "leading" })]}>
								<Text modifiers={[font({ size: 14, weight: "medium" })]}>{modelLabel}</Text>
								<Spacer />
								<Image systemName="chevron.down" size={iconSize.xs} />
							</HStack>
						}
						modifiers={[
							buttonStyle("plain"),
							frame({ maxWidth: 1000, alignment: "leading" }),
							tint(t.textSecondary),
							opacity(models.length || modelSelection === "__auto__" ? 1 : 0.45),
							accessibilityIdentifier("spawn-model"),
						]}
					>
						<Button
							label="Automatic"
							systemImage={modelSelection === "__auto__" ? "checkmark" : "wand.and.stars"}
							onPress={() => { haptics.select(); onSelectModel("__auto__"); }}
						/>
						{models.map((model) => (
							<Button
								key={model.id}
								label={model.label}
								systemImage={model.id === modelSelection ? "checkmark" : "cpu"}
								onPress={() => { haptics.select(); onSelectModel(model.id); }}
							/>
						))}
					</Menu>
				</HStack>

				</VStack>
			</Host>

			<Pressable
				accessibilityRole="button"
				accessibilityLabel={busy ? "Starting task" : "Start task"}
				accessibilityState={{ disabled }}
				testID="spawn-submit"
				disabled={disabled}
				onPress={() => { haptics.tap(); onSpawn(); }}
				style={({ pressed }) => [
					styles.spawnButton,
					{ backgroundColor: disabled ? t.bgElevatedHover : t.accent },
					pressed && !disabled && styles.spawnButtonPressed,
				]}
			>
				<RNText style={[styles.spawnLabel, { color: disabled ? t.textFaint : t.onAccent }]}>
					{busy ? "Starting…" : "Start task"}
				</RNText>
			</Pressable>
		</View>
	);
}

const styles = StyleSheet.create({
	stack: { width: "100%", height: 150, gap: space.hair },
	controlsHost: { width: "100%", height: 104 },
	spawnButton: {
		height: 44,
		borderRadius: 16,
		borderCurve: "continuous",
		alignItems: "center",
		justifyContent: "center",
	},
	spawnButtonPressed: { opacity: press.opacity, transform: [{ scale: press.scale }] },
	spawnLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
});

// The mark's box, and the chip it sits on when it needs one to stay visible.
// Same arithmetic as `AgentLogo`: a 16% inset and a 28% corner radius, so a
// chipped mark here and a chipped mark in a row are the same size and shape.
const MARK_SIZE = 20;
const CHIP_INSET = Math.round(MARK_SIZE * 0.16);
const CHIP_RADIUS = Math.round(MARK_SIZE * 0.28);

function HarnessImage({ uri, harness }: { uri?: string; harness: string }) {
	if (!uri) return <Image systemName="terminal" size={iconSize.sm} />;
	const mark = [resizable(), aspectRatio({ contentMode: "fit" }), frame({ width: MARK_SIZE - CHIP_INSET * 2, height: MARK_SIZE - CHIP_INSET * 2 })];
	const chip = chipColorFor(harness);
	// opencode's mark is pure white and cursor's likewise, so on the light theme
	// they disappeared entirely — this menu drew the raw asset, where the rest of
	// the app asks `chipColorFor` first. Most marks are colourful and render bare.
	return chip
		? (
			<Group modifiers={[frame({ width: MARK_SIZE, height: MARK_SIZE }), backgroundOverlay({ color: chip }), clipShape("roundedRectangle", CHIP_RADIUS)]}>
				<Image uiImage={uri} modifiers={mark} />
			</Group>
		)
		: <Image uiImage={uri} modifiers={[resizable(), aspectRatio({ contentMode: "fit" }), frame({ width: MARK_SIZE, height: MARK_SIZE })]} />;
}

function useHarnessLogoUris(agents: readonly SpawnComposerOption[]) {
	const ids = useMemo(() => agents.map((agent) => agent.id), [agents]);
	const [uris, setUris] = useState<Record<string, string>>({});
	useEffect(() => {
		let cancelled = false;
		void Promise.all(ids.map(async (id) => {
			const source = logoFor(id);
			if (!source) return [id, ""] as const;
			const asset = Asset.fromModule(source);
			await asset.downloadAsync();
			return [id, asset.localUri ?? asset.uri] as const;
		})).then((entries) => {
			if (!cancelled) setUris(Object.fromEntries(entries.filter(([, uri]) => Boolean(uri))));
		});
		return () => { cancelled = true; };
	}, [ids]);
	return uris;
}
