import { Feather } from "@expo/vector-icons";
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as DocumentPicker from "expo-document-picker";
import * as ImagePicker from "expo-image-picker";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Image, Keyboard, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { MicKey } from "../voice/MicKey";
import { useVoiceInput } from "../voice/useVoiceInput";
import type { ChatConfigOption, ChatImage, ChatResource, ChatSkill, ConversationSnapshot } from "./types";
import {
	composerSuggestionKey,
	findComposerSuggestion,
	replaceComposerSuggestion,
	type ComposerSuggestion,
} from "./composerSuggestions";
import { chatSheetRoute } from "./chatSheetRegistry";
import { composerSurfaceStyle } from "./chatChrome";
import { createRequestGate } from "./requestGate";

type Attachment =
	| { id: string; kind: "image"; name: string; bytes: number; image: ChatImage }
	| { id: string; kind: "resource"; name: string; bytes: number; resource: ChatResource };

const MAX_EMBEDDED_FILE_BYTES = 500_000;
const MAX_ATTACHMENTS = 8;
const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
const MAX_IMAGE_BYTES_TOTAL = 25 * 1024 * 1024;
const SUPPORTED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp"]);

export function ChatComposer({
	sessionId,
	snapshot,
	skills,
	filePaths,
	filePathsTruncated,
	onLoadSkills,
	onLoadFiles,
	configOptions,
	steerUnavailable,
	pending,
	error,
	onSend,
	onSteer,
	onInterrupt,
	onOpenSettings,
}: {
	sessionId: string;
	snapshot: ConversationSnapshot;
	skills: ChatSkill[];
	filePaths: string[];
	filePathsTruncated?: boolean;
	onLoadSkills(): Promise<ChatSkill[]>;
	onLoadFiles(): Promise<{ paths: string[]; truncated: boolean }>;
	configOptions?: ChatConfigOption[];
	steerUnavailable?: boolean;
	pending?: boolean;
	error?: string;
	onSend(text: string, attachments?: ChatImage[], resources?: ChatResource[]): Promise<void>;
	onSteer(text: string): Promise<void>;
	onInterrupt(): void;
	onOpenSettings(): void;
}) {
	const t = useTheme();
	const router = useRouter();
	const styles = useThemedStyles(makeStyles);
	const [text, setText] = useState("");
	const [cursor, setCursor] = useState(0);
	const [attachments, setAttachments] = useState<Attachment[]>([]);
	const [delivery, setDelivery] = useState<"steer" | "queue">("steer");
	const [localError, setLocalError] = useState<string>();
	const [submitting, setSubmitting] = useState(false);
	const active = snapshot.turns.some((turn) => turn.state === "running");
	const canSteer = snapshot.capabilities?.includes("steer") && !steerUnavailable && active;
	const canEmbedFiles = snapshot.capabilities?.includes("embedded_context");
	const steerEligible = canSteer && delivery === "steer" && attachments.length === 0;
	const stopped = snapshot.controller.state === "stopped";
	const draftKey = `ao.chat.draft.${sessionId}`;
	const openingSuggestion = useRef<string | undefined>(undefined);
	const pickerGate = useRef(createRequestGate()).current;
	const latestText = useRef(text);
	const latestCursor = useRef(cursor);
	latestText.current = text;
	latestCursor.current = cursor;
	useEffect(() => () => pickerGate.invalidate(), [pickerGate]);

	useEffect(() => { let mounted = true; void AsyncStorage.getItem(draftKey).then((value) => { if (mounted && value) setText((current) => current || value); }); return () => { mounted = false; }; }, [draftKey]);
	useEffect(() => { const timer = setTimeout(() => void (text ? AsyncStorage.setItem(draftKey, text) : AsyncStorage.removeItem(draftKey)), 250); return () => clearTimeout(timer); }, [draftKey, text]);

	const voice = useVoiceInput({ onTranscript: useCallback((spoken: string) => setText((old) => old ? `${old} ${spoken}` : spoken), []) });

	const submit = useCallback(async () => {
		if (submitting) return;
		const trimmed = text.trim();
		if (!trimmed && attachments.length === 0) return;
		setLocalError(undefined);
		setSubmitting(true);
		try {
			const images = attachments.filter((item): item is Extract<Attachment, { kind: "image" }> => item.kind === "image").map((item) => item.image);
			const resources = attachments.filter((item): item is Extract<Attachment, { kind: "resource" }> => item.kind === "resource").map((item) => item.resource);
			if (steerEligible) await onSteer(trimmed);
			else await onSend(trimmed, images.length ? images : undefined, resources.length ? resources : undefined);
			setText("");
			setAttachments([]);
			void AsyncStorage.removeItem(draftKey);
			Keyboard.dismiss();
			haptics.success();
		} catch (cause) {
			setLocalError(cause instanceof Error ? cause.message : String(cause));
			haptics.error();
		} finally { setSubmitting(false); }
	}, [text, attachments, steerEligible, onSteer, onSend, draftKey, submitting]);

	const addImage = async () => {
		setLocalError(undefined);
		try {
			const result = await ImagePicker.launchImageLibraryAsync({ mediaTypes: ["images"], base64: true, quality: 0.82, allowsMultipleSelection: true, selectionLimit: 4 });
			if (result.canceled) return;
			const errors = new Set<string>();
			const next = result.assets.flatMap((asset): Attachment[] => {
				if (!asset.base64) { errors.add("Some images couldn't be read and were skipped."); return []; }
				const mimeType = (asset.mimeType || "image/jpeg").toLowerCase();
				if (!SUPPORTED_IMAGE_TYPES.has(mimeType)) { errors.add("Only PNG, JPEG, GIF, WebP, and BMP images are supported."); return []; }
				const bytes = asset.fileSize ?? Math.floor(asset.base64.length * 0.75);
				if (bytes > MAX_IMAGE_BYTES) { errors.add("Each image must be under 10 MB."); return []; }
				return [{ id: `${asset.assetId ?? asset.uri}-${Date.now()}`, kind: "image", name: asset.fileName || "Image", bytes, image: { mimeType, data: asset.base64 } }];
			});
			const accepted = [...attachments];
			let imageBytes = accepted.filter((item) => item.kind === "image").reduce((sum, item) => sum + item.bytes, 0);
			for (const item of next) {
				if (accepted.length >= MAX_ATTACHMENTS) { errors.add(`You can attach up to ${MAX_ATTACHMENTS} items.`); break; }
				if (imageBytes + item.bytes > MAX_IMAGE_BYTES_TOTAL) { errors.add("Images must total under 25 MB."); break; }
				accepted.push(item);
				imageBytes += item.bytes;
			}
			setAttachments(accepted);
			if (accepted.length) setDelivery("queue");
			setLocalError(errors.size ? [...errors].join(" ") : undefined);
		} catch (cause) {
			setLocalError(cause instanceof Error ? cause.message : "Could not open the photo library.");
		}
	};
	const addFile = async () => {
		setLocalError(undefined);
		const result = await DocumentPicker.getDocumentAsync({ multiple: true, copyToCacheDirectory: true, type: ["text/*", "application/json", "application/xml", "application/yaml"] });
		if (result.canceled) return;
		try {
			const added: Attachment[] = [];
			for (const asset of result.assets) {
				if (attachments.length + added.length >= MAX_ATTACHMENTS) throw new Error(`You can attach up to ${MAX_ATTACHMENTS} items.`);
				if ((asset.size ?? 0) > MAX_EMBEDDED_FILE_BYTES) throw new Error(`${asset.name} is larger than 500 KB. Reference a worktree file with @ instead.`);
				const body = await fetch(asset.uri).then((response) => response.text());
				const bytes = new TextEncoder().encode(body).byteLength;
				if (bytes > MAX_EMBEDDED_FILE_BYTES) throw new Error(`${asset.name} is too large to embed. Reference a worktree file with @ instead.`);
				added.push({ id: `${asset.uri}-${Date.now()}`, kind: "resource", name: asset.name, bytes, resource: { uri: `mobile-attachment://${encodeURIComponent(asset.name)}`, name: asset.name, mimeType: asset.mimeType || "text/plain", text: body } });
			}
			setAttachments((old) => [...old, ...added]);
			if (added.length) setDelivery("queue");
		} catch (cause) { setLocalError(cause instanceof Error ? cause.message : String(cause)); }
	};

	const providerModel = configOptions?.find((option) => option.category === "model" || option.id === "model" || option.id === "agent");
	const providerModelLabel = providerModel?.type === "select" ? providerModel.choices.find((choice) => choice.value === providerModel.currentValue)?.name ?? providerModel.currentValue : undefined;
	const selectedModel = snapshot.modelReroute?.toModel || providerModelLabel || snapshot.settings.model;
	const openPicker = useCallback(async (kind: "skills" | "files", activeTrigger?: ComposerSuggestion) => {
		const request = pickerGate.begin();
		const loadedSkills = kind === "skills" ? await onLoadSkills() : skills;
		const loadedFiles = kind === "files" ? await onLoadFiles() : { paths: filePaths, truncated: Boolean(filePathsTruncated) };
		if (!pickerGate.isCurrent(request)) return;
		if (activeTrigger) {
			const currentTrigger = findComposerSuggestion(latestText.current, latestCursor.current);
			if (!currentTrigger || composerSuggestionKey(currentTrigger) !== composerSuggestionKey(activeTrigger)) return;
		}
		const pickerCatalog = kind === "skills"
			? { kind, skills: loadedSkills } as const
			: { kind, paths: loadedFiles.paths } as const;
		router.push(chatSheetRoute({ kind: "composer-picker", catalog: pickerCatalog, initialQuery: activeTrigger?.query, truncated: kind === "files" ? loadedFiles.truncated : undefined, onSelect: (value) => {
			setText((old) => {
				const next = activeTrigger ? replaceComposerSuggestion(old, activeTrigger, value) : `${old}${old && !/\s$/.test(old) ? " " : ""}${kind === "skills" ? `/${value}` : (/\s/.test(value) ? `"${value}"` : value)} `;
				setCursor(next.length);
				return next;
			});
		} }));
	}, [filePaths, filePathsTruncated, onLoadFiles, onLoadSkills, pickerGate, router, skills]);
	useEffect(() => {
		const suggestion = findComposerSuggestion(text, cursor);
		if (!suggestion) {
			pickerGate.invalidate();
			openingSuggestion.current = undefined;
			return;
		}
		const key = composerSuggestionKey(suggestion);
		if (openingSuggestion.current === key) return;
		openingSuggestion.current = key;
		void openPicker(suggestion.kind, suggestion);
	}, [cursor, openPicker, pickerGate, text]);
	return (
		<View style={styles.dock}>
			{voice.state === "starting" || voice.state === "recording" ? <View style={styles.voice}><Feather name="mic" size={12} color={t.red} /><Text style={styles.voiceText}>{voice.partial || (voice.state === "starting" ? "Keep holding…" : "Listening…")}</Text></View> : null}
			{attachments.length ? <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.attachments}>{attachments.map((item) => <View key={item.id} style={styles.attachment}>{item.kind === "image" ? <Image accessibilityIgnoresInvertColors source={{ uri: `data:${item.image.mimeType};base64,${item.image.data}` }} style={styles.attachmentImage} /> : <Feather name="file-text" size={13} color={t.blue} />}<Text numberOfLines={1} style={styles.attachmentName}>{item.name}</Text><Pressable hitSlop={7} accessibilityLabel={`Remove ${item.name}`} onPress={() => { haptics.tap(); setAttachments((old) => old.filter((candidate) => candidate.id !== item.id)); }}><Feather name="x" size={13} color={t.textTertiary} /></Pressable></View>)}</ScrollView> : null}
			{error || localError || voice.error ? <Text accessibilityRole="alert" style={styles.error}>{localError || error || voice.error}</Text> : null}
			<View style={[styles.composer, stopped && { opacity: 0.55 }]}>
				<TextInput
					accessibilityLabel="Message the agent"
					editable={!stopped}
					value={text}
					onChangeText={setText}
					onSelectionChange={(event) => setCursor(event.nativeEvent.selection.start)}
					placeholder={stopped ? "Agent is stopped" : active ? (steerEligible ? "Agent is working — this goes into its running turn" : "Agent is working — this sends when it finishes") : "Ask the agent…  / for skills, @ for files"}
					placeholderTextColor={t.textFaint}
					style={styles.input}
					multiline
					maxLength={40_000}
				/>
				{canSteer ? <DeliveryChoice value={attachments.length ? "queue" : delivery} onChange={setDelivery} steerDisabled={attachments.length > 0 || submitting} /> : null}
				<View style={styles.controls}>
					<IconButton icon="paperclip" label="Attach image" onPress={addImage} disabled={stopped} />
					{canEmbedFiles ? <IconButton icon="file-plus" label="Attach text file" onPress={addFile} disabled={stopped} /> : null}
					<IconButton icon="command" label="Skills" onPress={() => { void openPicker("skills"); }} disabled={stopped} />
					<IconButton icon="at-sign" label="Worktree files" onPress={() => { void openPicker("files"); }} disabled={stopped} />
					<Pressable accessibilityRole="button" accessibilityLabel="Turn settings" onPress={() => { haptics.tap(); onOpenSettings(); }} style={styles.settingLabel}><Feather name="cpu" size={13} color={t.textTertiary} /><Text numberOfLines={1} style={styles.settingText}>{selectedModel || "Default"}</Text></Pressable>
					<View style={{ flex: 1 }} />
					<MicKey state={voice.state} mode={voice.mode} onPressIn={voice.pressIn} onPressOut={voice.pressOut} />
					{active && !text.trim() ? <Pressable accessibilityRole="button" accessibilityLabel="Stop turn" onPress={() => { haptics.tap(); void onInterrupt(); }} style={styles.stop}><Feather name="square" size={14} color={t.textPrimary} /></Pressable> : <Pressable accessibilityRole="button" accessibilityLabel={steerEligible ? "Steer turn" : active ? "Queue message" : "Send message"} accessibilityState={{ disabled: stopped || pending || submitting }} disabled={stopped || pending || submitting || (!text.trim() && attachments.length === 0)} onPress={() => { haptics.tap(); void submit(); }} style={({ pressed }) => [styles.send, pressed && { opacity: 0.8 }, (stopped || pending || submitting || (!text.trim() && attachments.length === 0)) && { opacity: 0.35 }]}>{pending || submitting ? <ActivityIndicator size="small" color={t.onAccent} /> : <Feather name={steerEligible ? "corner-up-right" : "arrow-up"} size={17} color={t.onAccent} />}</Pressable>}
				</View>
			</View>
		</View>
	);
}

function IconButton({ icon, label, onPress, disabled }: { icon: keyof typeof Feather.glyphMap; label: string; onPress(): void; disabled?: boolean }) { const t = useTheme(); const styles = useThemedStyles(makeStyles); return <Pressable accessibilityRole="button" accessibilityLabel={label} accessibilityState={{ disabled }} disabled={disabled} hitSlop={7} onPress={() => { haptics.tap(); void onPress(); }} style={styles.iconButton}><Feather name={icon} size={17} color={disabled ? t.textFaint : t.textTertiary} /></Pressable>; }

function DeliveryChoice({ value, onChange, steerDisabled }: { value: "steer" | "queue"; onChange(value: "steer" | "queue"): void; steerDisabled?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View accessibilityRole="radiogroup" accessibilityLabel="Where this message goes while the agent is working" style={styles.deliveryChoice}>
		{(["steer", "queue"] as const).map((option) => {
			const disabled = option === "steer" && steerDisabled;
			const selected = value === option;
			return <Pressable key={option} accessibilityRole="radio" accessibilityState={{ checked: selected, disabled }} disabled={disabled} onPress={() => { haptics.select(); onChange(option); }} style={[styles.deliveryOption, selected && styles.deliveryOptionSelected, disabled && { opacity: 0.35 }]}><Text style={[styles.deliveryOptionText, selected && { color: t.textPrimary }]}>{option === "steer" ? "Steer this turn" : "Queue for next"}</Text></Pressable>;
		})}
		{steerDisabled ? <Text style={styles.deliveryHint}>Attachments start a new turn.</Text> : null}
	</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	dock: { paddingHorizontal: 10, paddingTop: 8, paddingBottom: 8, backgroundColor: t.bgSurface, borderTopWidth: 1, borderTopColor: t.borderSubtle },
	composer: composerSurfaceStyle(t),
	input: { minHeight: 44, maxHeight: 150, color: t.textPrimary, fontSize: 15, lineHeight: 21, padding: 0, textAlignVertical: "top" },
	controls: { minHeight: 42, flexDirection: "row", alignItems: "center", gap: 2, marginTop: 5 },
	iconButton: { width: 32, height: 36, alignItems: "center", justifyContent: "center" },
	settingLabel: { maxWidth: 105, height: 34, flexDirection: "row", alignItems: "center", gap: 5, paddingHorizontal: 5 },
	settingText: { color: t.textTertiary, fontSize: 10 },
	send: { width: 40, height: 40, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: t.blue, marginLeft: 7 },
	stop: { width: 40, height: 40, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: t.bgSubtle, borderWidth: 1, borderColor: t.borderDefault, marginLeft: 7 },
	attachments: { gap: 7, paddingBottom: 7 },
	attachment: { maxWidth: 180, flexDirection: "row", alignItems: "center", gap: 6, backgroundColor: t.bgElevated, borderRadius: 9, borderWidth: 1, borderColor: t.borderSubtle, paddingHorizontal: 9, paddingVertical: 7 },
	attachmentImage: { width: 28, height: 28, borderRadius: 6, backgroundColor: t.bgSubtle },
	attachmentName: { flexShrink: 1, color: t.textSecondary, fontSize: 11 },
	deliveryChoice: { minHeight: 29, flexDirection: "row", alignItems: "center", gap: 3, paddingHorizontal: 2, marginTop: 3 },
	deliveryOption: { borderRadius: 7, paddingHorizontal: 8, paddingVertical: 5 },
	deliveryOptionSelected: { backgroundColor: t.bgSubtle },
	deliveryOptionText: { color: t.textTertiary, fontSize: 10, fontWeight: "600" },
	deliveryHint: { flex: 1, textAlign: "right", color: t.textFaint, fontSize: 9 },
	error: { color: t.red, fontSize: 11, lineHeight: 15, marginBottom: 6, paddingHorizontal: 3 },
	voice: { flexDirection: "row", alignItems: "center", gap: 7, backgroundColor: t.tintRed, borderRadius: 9, paddingHorizontal: 10, paddingVertical: 7, marginBottom: 7 },
	voiceText: { flex: 1, color: t.textSecondary, fontSize: 11 },
});
