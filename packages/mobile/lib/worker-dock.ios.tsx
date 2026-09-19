import { Host } from "@expo/ui";
import { Button, GlassEffectContainer, Group, HStack, Image, Menu, Section, Spacer, TextField, useNativeState } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	Animation,
	animation,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	frame,
	labelStyle,
	opacity,
	padding,
	scaleEffect,
	textFieldStyle,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { memo, useEffect } from "react";
import { glassCircle, glassField } from "./glass";
import { GLASS_CIRCLE_SIZE } from "./native-header-button.ios";
import { haptics } from "./haptics";
import { duration } from "./tokens";
import { useTheme, useThemeState } from "./ThemeProvider";
import { workerProjectLabel, workerProjectOptions } from "./worker-controls";
import type { WorkerDockProps } from "./worker-dock";
import { workerDockVisibility } from "./worker-dock-layout";
import { workerSearchClearState } from "./worker-search";

export const WorkerDock = memo(function WorkerDock({
	query,
	onQueryChange,
	onSpawn,
	searchOpen,
	onSearchOpen,
	onSearchClose,
	projectFiltered,
	projects,
	selectedProjectId,
	onSelectProject,
}: WorkerDockProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const text = useNativeState(query);
	const clear = workerSearchClearState(query);
	const projectOptions = workerProjectOptions(projects);
	const selectedProjectLabel = workerProjectLabel(projects, selectedProjectId);
	const visibility = workerDockVisibility(searchOpen);

	useEffect(() => {
		if (text.get() !== query) text.set(query);
	}, [query, text]);

	return (
		<Host style={{ flex: 1, height: 52 }} colorScheme={scheme} seedColor={t.accent}>
			{/* The filter, the search field and spawn are three pieces of one dock.
			    A single container lets the system blend their glass as they meet. */}
			<GlassEffectContainer spacing={10}>
			<HStack spacing={10} modifiers={[frame({ height: 52, maxWidth: 1000 })]}>
				{visibility.showControls ? <Group
					modifiers={[
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						glassCircle(undefined, false),
					]}
				><Menu
					// The label is the hit target for a plain control, so it is drawn at the
					// circle's full size with the glyph centred inside it. Sized any smaller,
					// only taps that landed on the glyph opened the menu.
					label={
						<Image
							systemName="line.3.horizontal.decrease"
							size={18}
							color={projectFiltered ? t.accent : t.textSecondary}
							modifiers={[frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE })]}
						/>
					}
					modifiers={[
						buttonStyle("plain"),
						controlSize("large"),
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						accessibilityLabel("Worker options"),
						accessibilityIdentifier("worker-controls"),
					]}
				>
					<Section title="Worker list options">
						<Button
							label="Search"
							systemImage="magnifyingglass"
							onPress={() => {
								haptics.tap();
								onSearchOpen();
							}}
						/>
						<Menu label={`Projects · ${selectedProjectLabel}`} systemImage="folder">
							{projectOptions.map((project) => (
								<Button
									key={project.id}
									label={project.label}
									systemImage={project.id === selectedProjectId ? "checkmark" : "folder"}
									onPress={() => {
										haptics.select();
										onSelectProject(project.id);
									}}
								/>
							))}
						</Menu>
					</Section>
				</Menu></Group> : null}
				{visibility.showSearch ? (
					<HStack
						spacing={0}
						modifiers={[
							frame({ height: 44, maxWidth: 1000 }),
							glassField(44),
							animation(Animation.spring({ duration: duration.slow / 1000, bounce: 0 }), searchOpen),
						]}
					>
						<TextField
							text={text}
							autoFocus
							onTextChange={onQueryChange}
							onFocusChange={(focused) => {
								if (!focused && !text.get().trim()) onSearchClose();
							}}
							placeholder="Search workers"
							modifiers={[
								textFieldStyle("plain"),
								frame({ height: 48, maxWidth: 1000 }),
								padding({ leading: 15, trailing: 4 }),
								accessibilityIdentifier("worker-search"),
							]}
						/>
						<Button
							label="Clear and close search"
							systemImage="xmark.circle.fill"
							onPress={() => {
								onQueryChange("");
								onSearchClose();
							}}
							modifiers={[
								buttonStyle("plain"),
								labelStyle("iconOnly"),
								frame({ width: 38, height: 38 }),
								tint(t.textSecondary),
								opacity(clear.disabled ? 0.7 : clear.opacity),
								scaleEffect(clear.disabled ? 1 : clear.scale),
								animation(Animation.spring({ duration: duration.base / 1000, bounce: 0 }), !clear.disabled),
								accessibilityIdentifier("worker-search-clear"),
							]}
						/>
					</HStack>
				) : visibility.showControls && visibility.showSpawn ? (
					<Spacer />
				) : null}
				{visibility.showSpawn ? <Group
					modifiers={[
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						glassCircle(undefined, false),
					]}
				><Button
					onPress={onSpawn}
					modifiers={[
						buttonStyle("plain"),
						controlSize("large"),
						frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE }),
						accessibilityLabel("Spawn worker"),
						accessibilityIdentifier("spawn-worker"),
					]}
				>
					<Image
						systemName="plus"
						size={18}
						color={t.textPrimary}
						modifiers={[frame({ width: GLASS_CIRCLE_SIZE, height: GLASS_CIRCLE_SIZE })]}
					/>
				</Button></Group> : null}
			</HStack>
			</GlassEffectContainer>
		</Host>
	);
});
