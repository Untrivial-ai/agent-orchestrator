import { Feather } from "../icons";
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as DocumentPicker from "expo-document-picker";
import * as ImagePicker from "expo-image-picker";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Image, Keyboard, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import Animated, { useAnimatedStyle } from "react-native-reanimated";
import { useReanimatedKeyboardAnimation } from "react-native-keyboard-controller";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { fontScaleCap, iconSize, space, type } from "../tokens";
import { KEYBOARD_DOCK_GAP } from "../session/keyboardInset";
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
import { ComposerGlass, composerGlassSupported } from "./composer-glass";
import { ChatTurnSettingsControl } from "./ChatTurnSettingsControl";
import { composerDeliveryPresentation, composerDeliveryRoute, composerPrimaryAction, type ComposerDeliveryIntent } from "./composerDeliveryModel";
import { contextMeterModel } from "./contextMeter";
import { RequestCard } from "./RequestCard";
import type { RequestDockModel } from "./requestDockModel";
import { createRequestGate } from "./requestGate";
import { queuedConversationMessages } from "./timelineModel";

type Attachment =
	| { id: string; kind: "image"; name: string; bytes: number; image: ChatImage }
	| { id: string; kind: "resource"; name: string; bytes: number; resource: ChatResource };

const MAX_EMBEDDED_FILE_BYTES = 500_000;

/** The pill's resting height; it grows with the field up to COMPOSER_MAX_HEIGHT. */
export const COMPOSER_HEIGHT = 56;
export const COMPOSER_FIELD_HEIGHT = 44;
/**
 * Where the pill stops growing and the field starts scrolling instead.
 *
 * Main's numbers, kept because they were tuned against the same transcript: about
 * six lines of a reply's worth of prompt, so the composer can never swallow the
 * conversation it is writing into.
 */
const COMPOSER_MAX_HEIGHT = 150;
const COMPOSER_FIELD_MAX_HEIGHT = 138;
const COMPOSER_LINE_HEIGHT = type.subheadline.lineHeight;
/** The pill's corner, and so also the radius of the glass drawn behind it. */
const COMPOSER_RADIUS = 28;
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
	disabled,
	onSend,
	onSteer,
	onPromoteQueuedTurn,
	onCancelQueuedTurn,
	onInterrupt,
	onOpenSettings,
	onSettings,
	onConfigOption,
	restingInset,
	quotaActive,
	request,
	requestDismissed,
	onRequestDecide,
	onRequestResolveInput,
	onShowRequest,
	onDismissRequest,
	onRestoreRequest,
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
	disabled?: boolean;
	onSend(text: string, attachments?: ChatImage[], resources?: ChatResource[]): Promise<void>;
	onSteer(text: string): Promise<void>;
	onPromoteQueuedTurn(turnId: string): Promise<void>;
	onCancelQueuedTurn(turnId: string): Promise<void>;
	onInterrupt(): void;
	onOpenSettings(): void;
	onSettings(settings: TurnSettings): Promise<void>;
	onConfigOption(id: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>;
	/**
	 * What the dock owes with the keyboard down — the home-indicator inset. The
	 * dock keeps it at all times and closes the difference to the keyboard by
	 * itself; see `dockRise` below.
	 */
	restingInset: number;
	/** The account-quota banner is up, so the context meter stands down. */
	quotaActive?: boolean;
	/** A pending request, which takes the composer's place until it is answered. */
	request?: RequestDockModel | null;
	/** The user pushed the request aside to type instead. */
	requestDismissed?: boolean;
	onRequestDecide(requestId: string, decisionId: string): Promise<void>;
	onRequestResolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
	onShowRequest(sequence: number): void;
	onDismissRequest(): void;
	onRestoreRequest(): void;
}) {
	const t = useTheme();
	const router = useRouter();
	const styles = useThemedStyles(makeStyles);
	// The keyboard's own progress, 0 closed to 1 open. This is the same value the
	// keyboard is animating with, so the dock moves in lockstep with it.
	const keyboard = useReanimatedKeyboardAnimation();
	const dockRise = useAnimatedStyle(() => ({
		transform: [{ translateY: (restingInset - KEYBOARD_DOCK_GAP) * keyboard.progress.value }],
	}));
	const [text, setText] = useState("");
	const [cursor, setCursor] = useState(0);
	const [fieldHeight, setFieldHeight] = useState(COMPOSER_FIELD_HEIGHT);
	const [attachments, setAttachments] = useState<Attachment[]>([]);
	const [localError, setLocalError] = useState<string>();
	const [submitting, setSubmitting] = useState(false);
	const [promotingQueuedTurnId, setPromotingQueuedTurnId] = useState<string>();
	const [cancellingQueuedTurnId, setCancellingQueuedTurnId] = useState<string>();
	const [hiddenQueuedTurnIds, setHiddenQueuedTurnIds] = useState<Set<string>>(() => new Set());
	const active = Boolean(activeTurn(snapshot));
	const queuedMessages = useMemo(() => queuedConversationMessages(snapshot), [snapshot]);
	const visibleQueuedMessages = useMemo(() => queuedMessages.filter((entry) => !hiddenQueuedTurnIds.has(entry.turnId)), [hiddenQueuedTurnIds, queuedMessages]);
	// Absent below 70%: a permanent token gauge is chrome nobody reads.
	const contextMeter = contextMeterModel(snapshot.usage, Boolean(quotaActive));
	// A blocking question is the next thing to do, so it takes the input's place
	// rather than pointing at a card somewhere up the timeline.
	const requestCard = request && !requestDismissed ? (
		<RequestCard
			model={request}
			onDecide={onRequestDecide}
			onResolveInput={onRequestResolveInput}
			onShow={onShowRequest}
			onDismiss={onDismissRequest}
		/>
	) : null;
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
		if (submitting || pending || disabled) return;
		const trimmed = text.trim();
		if (!trimmed && attachments.length === 0) return;
		// Dismissed on the tap, not after the send lands. Waiting for the request
		// tied the keyboard's exit to the network, so it dropped whenever the
		// answer arrived — which read as the keyboard being taken away mid-sentence.
		Keyboard.dismiss();
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
			haptics.success();
		} catch (cause) {
			setLocalError(cause instanceof Error ? cause.message : String(cause));
			haptics.error();
		} finally { setSubmitting(false); }
	}, [text, attachments, steerEligible, onSteer, onSend, draftKey, submitting, pending, disabled]);

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
			setLocalError(cause instanceof Error ? cause.message : "Couldn't open your photo library.");
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
		// The dock holds its resting inset at all times and rides the keyboard's own
		// progress to close the difference, so its distance to the keyboard is
		// `KEYBOARD_DOCK_GAP` at every frame of the animation rather than only once
		// the keyboard has finished moving.
		<Animated.View style={[styles.dock, { paddingBottom: restingInset }, dockRise]}>
			{voice.state === "starting" || voice.state === "recording" ? <View style={styles.voice}><Feather name="mic" size={12} color={t.red} /><Text style={styles.voiceText}>{voice.partial || (voice.state === "starting" ? "Keep holding…" : "Listening…")}</Text></View> : null}
			{attachments.length ? <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.attachments}>{attachments.map((item) => <View key={item.id} style={styles.attachment}>{item.kind === "image" ? <Image accessibilityIgnoresInvertColors source={{ uri: `data:${item.image.mimeType};base64,${item.image.data}` }} style={styles.attachmentImage} /> : <Feather name="file-text" size={12} color={t.accent} />}<Text numberOfLines={1} style={styles.attachmentName}>{item.name}</Text><Pressable hitSlop={7} accessibilityLabel={`Remove ${item.name}`} onPress={() => { haptics.tap(); setAttachments((old) => old.filter((candidate) => candidate.id !== item.id)); }}><Feather name="x" size={12} color={t.textTertiary} /></Pressable></View>)}</ScrollView> : null}
			{/* Composer-local only. Conversation and action failures are banners above
			    the timeline; echoing them here showed one failure twice. */}
			{localError || voice.error ? <Text accessibilityRole="alert" style={styles.error}>{localError || voice.error}</Text> : null}
			<View style={styles.metaRow}>
				<View style={styles.settingsSlot}>
				<ChatTurnSettingsControl snapshot={snapshot} models={models} options={configOptions ?? []} disabled={disabled || stopped || pending || submitting} onSettings={onSettings} onOption={onConfigOption} onOpenFallback={onOpenSettings} />
				</View>
				{deliveryPresentation.showQueueNote ? <View style={styles.deliveryNote}>
					<Feather name="clock" size={12} color={t.textTertiary} />
					<Text numberOfLines={1} style={styles.deliveryNoteText}>{attachments.length ? "Attachments next" : "Sent after this"}</Text>
					{deliveryPresentation.showSteerAction ? <Pressable accessibilityRole="button" accessibilityLabel="Steer this turn now" disabled={disabled || submitting || pending} onPress={() => { haptics.tap(); void submit("steer"); }} style={({ pressed }) => [styles.steerAction, pressed && { opacity: 0.7 }]}><Feather name="corner-up-right" size={15} color={t.accent} /></Pressable> : null}
				</View> : null}
				{contextMeter ? <View accessibilityRole="progressbar" accessibilityLabel={`Context window ${contextMeter.percent}% used`} style={styles.contextMeter}>
					<View style={styles.contextTrack}>
						<View style={[styles.contextFill, { width: `${contextMeter.fillPercent}%`, backgroundColor: contextMeter.severity === "critical" ? t.red : t.amber }]} />
					</View>
					<Text numberOfLines={1} maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.contextText, { color: contextMeter.severity === "critical" ? t.red : t.amber }]}>{contextMeter.label}</Text>
				</View> : null}
			</View>
			{visibleQueuedMessages.length ? <View accessibilityRole="list" style={styles.queueDock}>
				{visibleQueuedMessages.map((entry, index) => {
					const promoting = promotingQueuedTurnId === entry.turnId;
					const cancelling = cancellingQueuedTurnId === entry.turnId;
					const queueActionPending = Boolean(promotingQueuedTurnId || cancellingQueuedTurnId);
					return <View key={entry.turnId} style={[styles.queueRow, index > 0 && styles.queueRowDivider]}>
						<Feather name="corner-down-right" size={15} color={t.textTertiary} />
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
							{promoting ? <ActivityIndicator size="small" color={t.accent} /> : <Feather name="corner-up-right" size={15} color={t.accent} />}
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
							{cancelling ? <ActivityIndicator size="small" color={t.textTertiary} /> : <Feather name="x" size={15} color={t.textTertiary} />}
						</Pressable>
					</View>;
				})}
			</View> : null}
			{request && !requestCard ? <Pressable
				accessibilityRole="button"
				accessibilityLabel={`${request.title}. Answer it`}
				onPress={() => { haptics.tap(); onRestoreRequest(); }}
				style={({ pressed }) => [styles.restore, pressed && { opacity: 0.6 }]}
			>
				<Feather name={request.kind === "approval" ? "shield" : "message-circle"} size={12} color={t.amber} />
				<Text numberOfLines={1} maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.restoreText}>{request.title}</Text>
				<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.restoreAction}>Answer</Text>
			</Pressable> : null}
			{requestCard ?? <View
				style={[styles.composer, stopped && { opacity: 0.55 }]}
			>
				<ComposerGlass radius={COMPOSER_RADIUS} />
				<ChatAttachmentMenu disabled={stopped} canAttachFile={Boolean(canEmbedFiles)} onChoosePhoto={() => void addImage()} onChooseFile={() => void addFile()} />
				<TextInput
					accessibilityLabel="Message the agent"
					editable={!stopped}
					value={text}
					onChangeText={setText}
					onSelectionChange={(event) => setCursor(event.nativeEvent.selection.start)}
					onContentSizeChange={(event) => {
						// Native contentSize already includes the TextInput's vertical
						// padding. Treat it as the field's full height; converting it to
						// lines counted the padding as an extra line on the first render.
						const contentHeight = Math.ceil(event.nativeEvent.contentSize.height);
						setFieldHeight(Math.max(COMPOSER_FIELD_HEIGHT, Math.min(COMPOSER_FIELD_MAX_HEIGHT, contentHeight)));
					}}
					placeholder={stopped ? "Agent is stopped" : deliveryPresentation.placeholder}
					placeholderTextColor={t.textFaint}
					style={[styles.input, { height: fieldHeight }]}
					multiline
					maxLength={40_000}
				/>
				<MicKey variant="plain" size={44} glyphSize={iconSize.lg} state={voice.state} mode={voice.mode} onPressIn={voice.pressIn} onPressOut={voice.pressOut} />
				{primaryAction === "stop" ? <Pressable accessibilityRole="button" accessibilityLabel="Stop turn" accessibilityState={{ busy: interrupting, disabled: disabled || interrupting }} disabled={disabled || interrupting} onPress={() => { haptics.tap(); void onInterrupt(); }} style={[styles.stop, (disabled || interrupting) && { opacity: 0.55 }]}>{interrupting ? <ActivityIndicator size="small" color={t.textPrimary} /> : <Feather name="square" size={12} color={t.textPrimary} />}</Pressable> : <Pressable accessibilityRole="button" accessibilityLabel={active ? "Queue message" : "Send message"} accessibilityState={{ disabled: disabled || stopped || pending || submitting }} disabled={disabled || stopped || pending || submitting || (!text.trim() && attachments.length === 0)} onPress={() => { haptics.tap(); void submit("send"); }} style={({ pressed }) => [styles.send, pressed && { opacity: 0.8 }, (disabled || stopped || pending || submitting || (!text.trim() && attachments.length === 0)) && { opacity: 0.35 }]}>{pending || submitting ? <ActivityIndicator size="small" color={t.bgBase} /> : <Feather name="arrow-up" size={17} color={t.bgBase} />}</Pressable>}
			</View>}
		</Animated.View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	dock: { paddingHorizontal: space.md, paddingTop: space.xs, gap: space.xs, backgroundColor: t.bgBase },
	// Three things can share this row — the turn settings, the queued-message note
	// and the context meter — and the settings label is the only one that can be
	// long. It is the one that yields: `flex: 1` with `minWidth: 0` to allow the
	// squeeze, and `overflow: "hidden"` so a label that does not truncate cleanly
	// cannot paint over its neighbours. The `gap` is the floor under that: even
	// when everything fits, the two never touch.
	metaRow: { width: "100%", height: 44, flexDirection: "row", alignItems: "center", gap: space.sm },
	settingsSlot: { flex: 1, minWidth: 0, height: 44, alignItems: "flex-start", justifyContent: "center", overflow: "hidden" },
	// One pill for the whole row: attach, the field, dictation, and the one filled
	// control that commits. The pill's radius is half its resting height, so a
	// single line reads as a capsule and a long message grows a rounded panel —
	// no second border, no second surface.
	//
	// The fill goes transparent where the glass layer is drawing behind it: an
	// opaque pill under a material is the one arrangement that turns glass grey.
	//
	// Bottom-align the controls so attach, mic and send stay anchored while the
	// text field grows above them. The pill grows around the row; the glass fills
	// that pill rather than receiving a separately measured height.
	composer: { minHeight: COMPOSER_HEIGHT, maxHeight: COMPOSER_MAX_HEIGHT, flexDirection: "row", alignItems: "flex-end", gap: space.xxs, paddingHorizontal: space.xs, paddingVertical: space.xs, backgroundColor: composerGlassSupported ? "transparent" : t.bgElevated, borderRadius: COMPOSER_RADIUS, borderCurve: "continuous" },
	// The native content-size event grows this from its one-line resting height;
	// at the cap, the multiline field scrolls while the controls remain in place.
	input: { fontFamily: "Geist_400Regular", flex: 1, minHeight: COMPOSER_FIELD_HEIGHT, maxHeight: COMPOSER_FIELD_MAX_HEIGHT, color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: COMPOSER_LINE_HEIGHT, paddingVertical: space.md, textAlignVertical: "top" },
	send: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.accent },
	stop: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: t.bgSubtle, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	attachments: { gap: space.xs, paddingBottom: space.xs },
	attachment: { maxWidth: 180, flexDirection: "row", alignItems: "center", gap: space.xs, backgroundColor: t.bgElevated, borderRadius: 8, borderWidth: 1, borderColor: t.borderSubtle, paddingHorizontal: space.sm, paddingVertical: space.xs },
	attachmentImage: { width: 28, height: 28, borderRadius: 4, backgroundColor: t.bgSubtle },
	attachmentName: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textSecondary, fontSize: type.caption2.fontSize },
	// The way back to a request the user pushed aside to type instead.
	restore: { flexDirection: "row", alignItems: "center", gap: space.xs, paddingVertical: space.xs, paddingHorizontal: space.sm, borderRadius: 12, backgroundColor: t.tintAmber },
	restoreText: { fontFamily: "Geist_600SemiBold", flex: 1, minWidth: 0, color: t.amber, fontSize: type.caption1.fontSize, fontWeight: "600" },
	restoreAction: { fontFamily: "Geist_600SemiBold", color: t.amber, fontSize: type.caption1.fontSize, fontWeight: "600", textDecorationLine: "underline" },
	// Sits at the end of the meta row; the bar carries the reading, the label names it.
	contextMeter: { flexShrink: 0, flexDirection: "row", alignItems: "center", gap: space.xs, height: 32, paddingLeft: space.sm },
	contextTrack: { width: 34, height: 4, borderRadius: 2, overflow: "hidden", backgroundColor: t.bgSubtle },
	contextFill: { height: 4, borderRadius: 2 },
	contextText: { fontFamily: "Geist_600SemiBold", fontSize: type.caption2.fontSize, fontWeight: "600" },
	deliveryNote: { flexShrink: 0, maxWidth: "46%", height: 32, flexDirection: "row", alignItems: "center", justifyContent: "flex-end", gap: space.xxs, paddingRight: space.xxs },
	deliveryNoteText: { fontFamily: "Geist_600SemiBold", flexShrink: 1, color: t.textTertiary, fontSize: type.caption2.fontSize, fontWeight: "600" },
	steerAction: { width: 30, height: 30, alignItems: "center", justifyContent: "center", borderRadius: 16, backgroundColor: t.accentTint },
	queueDock: { overflow: "hidden", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, borderRadius: 16, borderCurve: "continuous" },
	queueRow: { minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.sm, paddingLeft: space.md, paddingRight: space.xxs },
	queueRowDivider: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	queueText: { fontFamily: "Geist_500Medium", flex: 1, color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, fontWeight: "500" },
	queueSteer: { width: 40, height: 40, alignItems: "center", justifyContent: "center", borderRadius: 20 },
	queueDelete: { width: 40, height: 40, borderRadius: 20, alignItems: "center", justifyContent: "center" },
	error: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginBottom: space.xs, paddingHorizontal: space.hair },
	voice: { flexDirection: "row", alignItems: "center", gap: space.xs, backgroundColor: t.tintRed, borderRadius: 8, paddingHorizontal: space.sm, paddingVertical: space.xs, marginBottom: space.xs },
	voiceText: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize },
});
