import { Host } from "@expo/ui";
import { Button, GlassEffectContainer, Group, HStack, Image, Spacer, Text, TextField, VStack, useNativeState, type TextFieldRef } from "@expo/ui/swift-ui";
import {
	accessibilityLabel,
	autocorrectionDisabled,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	disabled,
	font,
	frame,
	glassEffect,
	keyboardType,
	labelStyle,
	lineLimit,
	multilineTextAlignment,
	onSubmit,
	opacity,
	submitLabel,
	textFieldStyle,
	textInputAutocapitalization,
} from "@expo/ui/swift-ui/modifiers";
import { useEffect, useRef, useState } from "react";
import { StyleSheet, useWindowDimensions, View } from "react-native";
import { useThemeState } from "../ThemeProvider";
import { displayBrowserUrl } from "./browserUrl";
import { browserGlassSupported } from "./BrowserGlassSurface";
import { BrowserToolbar as BrowserToolbarFallback, type BrowserToolbarProps } from "./BrowserToolbarFallback";

const CONTROL_SIZE = 44;

/** iOS 26 browser chrome whose material, controls and ink share one native hierarchy. */
export function BrowserToolbar(props: BrowserToolbarProps) {
	const { scheme } = useThemeState();
	const { width } = useWindowDimensions();
	const toolbarWidth = Math.max(240, width - 32);
	const addressTextWidth = toolbarWidth - 96;
	const chromeScheme = props.contentColorScheme ?? scheme;
	const [editing, setEditing] = useState(false);
	const draft = useNativeState(props.url);
	const inputRef = useRef<TextFieldRef>(null);

	useEffect(() => {
		if (!editing && draft.get() !== props.url) draft.set(props.url);
	}, [draft, editing, props.url]);

	if (!browserGlassSupported) return <BrowserToolbarFallback {...props} />;

	const submit = () => {
		const value = draft.get().trim();
		setEditing(false);
		if (value) props.onSubmitUrl(value);
	};

	return (
		<View style={styles.toolbar}>
			<Host style={[styles.addressHost, { width: toolbarWidth }]} colorScheme={chromeScheme}>
				<Group modifiers={[
					frame({ width: toolbarWidth, height: CONTROL_SIZE }),
					glassEffect({ glass: { variant: "regular" }, shape: "roundedRectangle", cornerRadius: CONTROL_SIZE / 2 }),
				]}>
					<HStack spacing={0} modifiers={[frame({ width: toolbarWidth, height: CONTROL_SIZE })]}>
						<Image systemName={props.url.startsWith("https://") ? "lock" : "globe"} size={15} modifiers={[frame({ width: 48, height: CONTROL_SIZE })]} />
						{editing ? (
							<TextField
								ref={inputRef}
								text={draft}
								autoFocus
								placeholder="Enter a URL"
								onFocusChange={(focused) => {
									if (!focused) {
										setEditing(false);
										return;
									}
									requestAnimationFrame(() => inputRef.current?.setSelection(0, draft.get().length));
								}}
								modifiers={[
									textFieldStyle("plain"),
									frame({ width: addressTextWidth, height: CONTROL_SIZE }),
									multilineTextAlignment("center"),
									keyboardType("url"),
									textInputAutocapitalization("never"),
									autocorrectionDisabled(),
									submitLabel("go"),
									onSubmit(submit),
								]}
							/>
						) : (
							<Button
								onPress={() => { draft.set(props.url); setEditing(true); }}
								modifiers={[buttonStyle("plain"), frame({ width: addressTextWidth, height: CONTROL_SIZE }), accessibilityLabel(props.url ? "Edit browser URL" : "Enter browser URL")]}
							>
								<VStack spacing={0} modifiers={[frame({ width: addressTextWidth, height: CONTROL_SIZE })]}>
									<Text modifiers={[font({ size: 14, weight: "semibold" }), lineLimit(1), multilineTextAlignment("center")]}>{props.title || displayBrowserUrl(props.url) || "Enter a URL"}</Text>
									{props.url ? <Text modifiers={[font({ size: 11, design: "monospaced" }), lineLimit(1), opacity(0.62), multilineTextAlignment("center")]}>{displayBrowserUrl(props.url)}</Text> : null}
								</VStack>
							</Button>
						)}
						<Button
							onPress={props.loading ? props.onStop : props.onReload}
							modifiers={[buttonStyle("plain"), labelStyle("iconOnly"), frame({ width: 48, height: CONTROL_SIZE }), disabled(!props.url && !props.loading), opacity(!props.url && !props.loading ? 0.45 : 1), accessibilityLabel(props.loading ? "Stop loading" : "Reload")]}
						>
							<Image systemName={props.loading ? "xmark" : "arrow.clockwise"} size={17} />
						</Button>
					</HStack>
				</Group>
			</Host>

			<Host style={[styles.controlsHost, { width: toolbarWidth }]} colorScheme={chromeScheme}>
				<GlassEffectContainer spacing={18}>
					<HStack spacing={0} modifiers={[frame({ width: toolbarWidth, height: CONTROL_SIZE })]}>
						<BrowserButton label="Back" symbol="chevron.left" enabled={props.canGoBack} onPress={props.onBack} />
						<Spacer />
						<BrowserButton label="Forward" symbol="chevron.right" enabled={props.canGoForward} onPress={props.onForward} />
						<Spacer />
						<BrowserButton label="Open externally" symbol="arrow.up.right.square" enabled={props.canOpenExternal} onPress={props.onOpenExternal} />
						<Spacer />
						<BrowserButton label="Share URL" symbol="square.and.arrow.up" enabled={Boolean(props.url)} onPress={props.onShare} />
					</HStack>
				</GlassEffectContainer>
			</Host>
		</View>
	);
}

function BrowserButton({ label, symbol, enabled, onPress }: { label: string; symbol: "chevron.left" | "chevron.right" | "arrow.up.right.square" | "square.and.arrow.up"; enabled: boolean; onPress: () => void }) {
	return (
		<Button
			onPress={onPress}
			modifiers={[
				buttonStyle("glass"),
				buttonBorderShape("circle"),
				controlSize("large"),
				labelStyle("iconOnly"),
				frame({ width: CONTROL_SIZE, height: CONTROL_SIZE }),
				disabled(!enabled),
				opacity(enabled ? 1 : 0.42),
				accessibilityLabel(label),
			]}
		>
			<Image systemName={symbol} size={19} />
		</Button>
	);
}

const styles = StyleSheet.create({
	toolbar: { gap: 10, paddingHorizontal: 16, paddingTop: 8, paddingBottom: 8 },
	addressHost: { height: CONTROL_SIZE },
	controlsHost: { height: CONTROL_SIZE },
});

export type { BrowserToolbarProps } from "./BrowserToolbarFallback";
