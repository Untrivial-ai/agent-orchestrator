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
import { activeTurn, type ChatConfigOption, type ChatImage, type ChatModel, type ChatResource, type ChatSkill, type ConversationSnapshot, type TurnSettings } from "./types";
import {
	composerSuggestionKey,
	findComposerSuggestion,
	replaceComposerSuggestion,
	type ComposerSuggestion,
} from "./composerSuggestions";
import { chatSheetRoute } from "./chatSheetRegistry";
import { ChatAttachmentMenu } from "./ChatAttachmentMenu";
import { ChatTurnSettingsControl } from "./ChatTurnSettingsControl";
import { composerDeliveryPresentation, composerDeliveryRoute, composerPrimaryAction, type ComposerDeliveryIntent } from "./composerDeliveryModel";
import { createRequestGate } from "./requestGate";
import { queuedConversationMessages } from "./timelineModel";

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
	models,
	steerUnavailable,
	pending,
	interrupting,
	error,
	onSend,
	onSteer,
	onPromoteQueuedTurn,
	onCancelQueuedTurn,
	onInterrupt,
	onOpenSettings,
	onSettings,
	onConfigOption,
	bottomInset,
}: {
	sessionId: string;
	snapshot: ConversationSnapshot;
	skills: ChatSkill[];
	filePaths: string[];
	filePathsTruncated?: boolean;
	onLoadSkills(): Promise<ChatSkill[]>;
	onLoadFiles(): Promise<{ paths: string[]; truncated: boolean }>;
	configOptions?: ChatConfigOption[];
	models: ChatModel[];
	steerUnavailable?: boolean;
	pending?: boolean;
	interrupting?: boolean;
	error?: string;
	onSend(text: string, attachments?: ChatImage[], resources?: ChatResource[]): Promise<void>;
	onSteer(text: string): Promise<void>;
	onPromoteQueuedTurn(turnId: string): Promise<void>;
	onCancelQueuedTurn(turnId: string): Promise<void>;
	onInterrupt(): void;
	onOpenSettings(): void;
	onSettings(settings: TurnSettings): Promise<void>;
	onConfigOption(id: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>;
	bottomInset: number;
}) {
	const t = useTheme();
	const router = useRouter();
	const styles = useThemedStyles(makeStyles);
	const [text, setText] = useState("");
	const [cursor, setCursor] = useState(0);
	const [attachments, setAttachments] = useState<Attachment[]>([]);
	const [localError, setLocalError] = useState<string>();
	const [submitting, setSubmitting] = useState(false);
	const [promotingQueuedTurnId, setPromotingQueuedTurnId] = useState<string>();
	const [cancellingQueuedTurnId, setCancellingQueuedTurnId] = useState<string>();
	const [hiddenQueuedTurnIds, setHiddenQueuedTurnIds] = useState<Set<string>>(() => new Set());
	const active = Boolean(activeTurn(snapshot));
	const queuedMessages = useMemo(() => queuedConversationMessages(snapshot), [snapshot]);
	const visibleQueuedMessages = useMemo(() => queuedMessages.filter((entry) => !hiddenQueuedTurnIds.has(entry.turnId)), [hiddenQueuedTurnIds, queuedMessages]);
	const canSteer = snapshot.capabilities?.includes("steer") && !steerUnavailable && active;
	const canEmbedFiles = snapshot.capabilities?.includes("embedded_context");
	const hasDraft = Boolean(text.trim());
	const primaryAction = composerPrimaryAction({ active, hasDraft, hasAttachments: attachments.length > 0 });
	const steerEligible = Boolean(canSteer && hasDraft && attachments.length === 0);
	const deliveryPresentation = composerDeliveryPresentation({ active, canSteer: Boolean(canSteer), hasDraft, hasAttachments: attachments.length > 0, hasQueued: visibleQueuedMessages.length > 0 });
	const stopped = snapshot.controller.state === "stopped";
	const draftKey = `ao.chat.draft.${sessionId}`;
	const openingSuggestion = useRef<string | undefined>(undefined);
	const pickerGate = useRef(createRequestGate()).current;
	const latestText = useRef(text);
	const latestCursor = useRef(cursor);
	latestText.current = text;
	latestCursor.current = cursor;
	useEffect(() => () => pickerGate.invalidate(), [pickerGate]);
	useEffect(() => {
		const queuedIds = new Set(queuedMessages.map((entry) => entry.turnId));
		setHiddenQueuedTurnIds((current) => {
			const next = new Set([...current].filter((id) => queuedIds.has(id)));
			return next.size === current.size ? current : next;
		});
	}, [queuedMessages]);

	useEffect(() => { let mounted = true; void AsyncStorage.getItem(draftKey).then((value) => { if (mounted && value) setText((current) => current || value); }); return () => { mounted = false; }; }, [draftKey]);
	useEffect(() => { const timer = setTimeout(() => void (text ? AsyncStorage.setItem(draftKey, text) : AsyncStorage.removeItem(draftKey)), 250); return () => clearTimeout(timer); }, [draftKey, text]);

	const voice = useVoiceInput({ onTranscript: useCallback((spoken: string) => setText((old) => old ? `${old} ${spoken}` : spoken), []) });

	const submit = useCallback(async (intent: ComposerDeliveryIntent = "send") => {
		if (submitting) return;
		const trimmed = text.trim();
		if (!trimmed && attachments.length === 0) return;
		setLocalError(undefined);
		setSubmitting(true);
		try {
			const images = attachments.filter((item): item is Extract<Attachment, { kind: "image" }> => item.kind === "image").map((item) => item.image);
			const resources = attachments.filter((item): item is Extract<Attachment, { kind: "resource" }> => item.kind === "resource").map((item) => item.resource);
			const route = composerDeliveryRoute(intent, steerEligible);
			if (route === "steer") await onSteer(trimmed);
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
		} catch (cause) { setLocalError(cause instanceof Error ? cause.message : String(cause)); }
	};

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
		<View style={[styles.dock, { paddingBottom: bottomInset }]}>
			{voice.state === "starting" || voice.state === "recording" ? <View style={styles.voice}><Feather name="mic" size={12} color={t.red} /><Text style={styles.voiceText}>{voice.partial || (voice.state === "starting" ? "Keep holding…" : "Listening…")}</Text></View> : null}
			{attachments.length ? <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.attachments}>{attachments.map((item) => <View key={item.id} style={styles.attachment}>{item.kind === "image" ? <Image accessibilityIgnoresInvertColors source={{ uri: `data:${item.image.mimeType};base64,${item.image.data}` }} style={styles.attachmentImage} /> : <Feather name="file-text" size={13} color={t.blue} />}<Text numberOfLines={1} style={styles.attachmentName}>{item.name}</Text><Pressable hitSlop={7} accessibilityLabel={`Remove ${item.name}`} onPress={() => { haptics.tap(); setAttachments((old) => old.filter((candidate) => candidate.id !== item.id)); }}><Feather name="x" size={13} color={t.textTertiary} /></Pressable></View>)}</ScrollView> : null}
			{error || localError || voice.error ? <Text accessibilityRole="alert" style={styles.error}>{localError || error || voice.error}</Text> : null}
			<View style={styles.metaRow}>
				<View style={styles.settingsSlot}>
					<ChatTurnSettingsControl snapshot={snapshot} models={models} options={configOptions ?? []} disabled={stopped || pending || submitting} onSettings={onSettings} onOption={onConfigOption} onOpenFallback={onOpenSettings} />
				</View>
				{deliveryPresentation.showQueueNote ? <View style={styles.deliveryNote}>
					<Feather name="clock" size={12} color={t.textTertiary} />
					<Text numberOfLines={1} style={styles.deliveryNoteText}>{attachments.length ? "Attachments next" : "Sent after this"}</Text>
					{deliveryPresentation.showSteerAction ? <Pressable accessibilityRole="button" accessibilityLabel="Steer this turn now" disabled={submitting || pending} onPress={() => { haptics.tap(); void submit("steer"); }} style={({ pressed }) => [styles.steerAction, pressed && { opacity: 0.7 }]}><Feather name="corner-up-right" size={14} color={t.blue} /></Pressable> : null}
				</View> : null}
			</View>
			{visibleQueuedMessages.length ? <View accessibilityRole="list" style={styles.queueDock}>
				{visibleQueuedMessages.map((entry, index) => {
					const promoting = promotingQueuedTurnId === entry.turnId;
					const cancelling = cancellingQueuedTurnId === entry.turnId;
					const queueActionPending = Boolean(promotingQueuedTurnId || cancellingQueuedTurnId);
					return <View key={entry.turnId} style={[styles.queueRow, index > 0 && styles.queueRowDivider]}>
						<Feather name="corner-down-right" size={14} color={t.textTertiary} />
						<Text numberOfLines={1} style={styles.queueText}>{entry.message.text}</Text>
						{canSteer ? <Pressable
							accessibilityRole="button"
							accessibilityLabel={`Steer queued message: ${entry.message.text}`}
							accessibilityState={{ busy: promoting, disabled: queueActionPending }}
							disabled={queueActionPending}
							onPress={() => {
								haptics.tap();
								setPromotingQueuedTurnId(entry.turnId);
								setHiddenQueuedTurnIds((current) => new Set(current).add(entry.turnId));
								void onPromoteQueuedTurn(entry.turnId)
									.then(() => haptics.success())
									.catch(() => {
										setHiddenQueuedTurnIds((current) => {
											const next = new Set(current);
											next.delete(entry.turnId);
											return next;
										});
										haptics.error();
									})
									.finally(() => setPromotingQueuedTurnId(undefined));
							}}
							style={({ pressed }) => [styles.queueSteer, pressed && { opacity: 0.55 }]}
						>
							{promoting ? <ActivityIndicator size="small" color={t.blue} /> : <Feather name="corner-up-right" size={15} color={t.blue} />}
						</Pressable> : null}
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={`Delete queued message: ${entry.message.text}`}
							accessibilityState={{ busy: cancelling, disabled: queueActionPending }}
							disabled={queueActionPending}
							onPress={() => {
								haptics.tap();
								setCancellingQueuedTurnId(entry.turnId);
								void onCancelQueuedTurn(entry.turnId)
									.then(() => haptics.success())
									.catch(() => haptics.error())
									.finally(() => setCancellingQueuedTurnId(undefined));
							}}
							style={({ pressed }) => [styles.queueDelete, pressed && { opacity: 0.55 }]}
						>
							{cancelling ? <ActivityIndicator size="small" color={t.textTertiary} /> : <Feather name="x" size={16} color={t.textTertiary} />}
						</Pressable>
					</View>;
				})}
			</View> : null}
			<View style={[styles.composer, stopped && { opacity: 0.55 }]}>
				<ChatAttachmentMenu disabled={stopped} canAttachFile={Boolean(canEmbedFiles)} onChoosePhoto={() => void addImage()} onChooseFile={() => void addFile()} />
				<TextInput
					accessibilityLabel="Message the agent"
					editable={!stopped}
					value={text}
					onChangeText={setText}
					onSelectionChange={(event) => setCursor(event.nativeEvent.selection.start)}
					placeholder={stopped ? "Agent is stopped" : deliveryPresentation.placeholder}
					placeholderTextColor={t.textFaint}
					style={styles.input}
					multiline
					maxLength={40_000}
				/>
				<MicKey circular size={42} state={voice.state} mode={voice.mode} onPressIn={voice.pressIn} onPressOut={voice.pressOut} />
				{primaryAction === "stop" ? <Pressable accessibilityRole="button" accessibilityLabel="Stop turn" accessibilityState={{ busy: interrupting }} disabled={interrupting} onPress={() => { haptics.tap(); void onInterrupt(); }} style={[styles.stop, interrupting && { opacity: 0.55 }]}>{interrupting ? <ActivityIndicator size="small" color={t.textPrimary} /> : <Feather name="square" size={13} color={t.textPrimary} />}</Pressable> : <Pressable accessibilityRole="button" accessibilityLabel={active ? "Queue message" : "Send message"} accessibilityState={{ disabled: stopped || pending || submitting }} disabled={stopped || pending || submitting || (!text.trim() && attachments.length === 0)} onPress={() => { haptics.tap(); void submit("send"); }} style={({ pressed }) => [styles.send, pressed && { opacity: 0.8 }, (stopped || pending || submitting || (!text.trim() && attachments.length === 0)) && { opacity: 0.35 }]}>{pending || submitting ? <ActivityIndicator size="small" color={t.onAccent} /> : <Feather name="arrow-up" size={17} color={t.onAccent} />}</Pressable>}
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	dock: { paddingHorizontal: 12, paddingTop: 7, gap: 6, backgroundColor: t.bgBase },
	metaRow: { width: "100%", height: 44, flexDirection: "row", alignItems: "center" },
	settingsSlot: { flex: 1, minWidth: 0, height: 44, alignItems: "flex-start", justifyContent: "center" },
	composer: { minHeight: 54, maxHeight: 150, flexDirection: "row", alignItems: "flex-end", gap: 3, padding: 5, backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, borderRadius: 27, borderCurve: "continuous" },
	input: { flex: 1, minHeight: 42, maxHeight: 138, color: t.textPrimary, fontSize: 15, lineHeight: 21, paddingHorizontal: 3, paddingVertical: 10, textAlignVertical: "top" },
	send: { width: 42, height: 42, borderRadius: 21, alignItems: "center", justifyContent: "center", backgroundColor: t.blue },
	stop: { width: 42, height: 42, borderRadius: 21, alignItems: "center", justifyContent: "center", backgroundColor: t.bgSubtle, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	attachments: { gap: 7, paddingBottom: 7 },
	attachment: { maxWidth: 180, flexDirection: "row", alignItems: "center", gap: 6, backgroundColor: t.bgElevated, borderRadius: 9, borderWidth: 1, borderColor: t.borderSubtle, paddingHorizontal: 9, paddingVertical: 7 },
	attachmentImage: { width: 28, height: 28, borderRadius: 6, backgroundColor: t.bgSubtle },
	attachmentName: { flexShrink: 1, color: t.textSecondary, fontSize: 11 },
	deliveryNote: { maxWidth: "46%", height: 32, flexDirection: "row", alignItems: "center", justifyContent: "flex-end", gap: 5, paddingRight: 5 },
	deliveryNoteText: { flexShrink: 1, color: t.textTertiary, fontSize: 10, fontWeight: "600" },
	steerAction: { width: 30, height: 30, alignItems: "center", justifyContent: "center", borderRadius: 15, backgroundColor: t.tintBlue },
	queueDock: { overflow: "hidden", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, borderRadius: 16, borderCurve: "continuous" },
	queueRow: { minHeight: 44, flexDirection: "row", alignItems: "center", gap: 9, paddingLeft: 13, paddingRight: 4 },
	queueRowDivider: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	queueText: { flex: 1, color: t.textSecondary, fontSize: 13, lineHeight: 18, fontWeight: "500" },
	queueSteer: { width: 40, height: 40, alignItems: "center", justifyContent: "center", borderRadius: 20 },
	queueDelete: { width: 40, height: 40, borderRadius: 20, alignItems: "center", justifyContent: "center" },
	error: { color: t.red, fontSize: 11, lineHeight: 15, marginBottom: 6, paddingHorizontal: 3 },
	voice: { flexDirection: "row", alignItems: "center", gap: 7, backgroundColor: t.tintRed, borderRadius: 9, paddingHorizontal: 10, paddingVertical: 7, marginBottom: 7 },
	voiceText: { flex: 1, color: t.textSecondary, fontSize: 11 },
});
