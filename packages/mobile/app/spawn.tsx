import { useLocalSearchParams, useRouter } from "expo-router";
import { Feather } from "../lib/icons";
import BottomSheet, { BottomSheetView } from "@expo/ui/community/bottom-sheet";
import * as DocumentPicker from "expo-document-picker";
import { File } from "expo-file-system";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	InteractionManager,
	Platform,
	Pressable,
	ScrollView,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { KeyboardStickyView, useKeyboardState } from "react-native-keyboard-controller";
import { agentErrorCopy } from "../lib/agentError";
import { defaultAgent, rankAgents } from "../lib/agentPicker";
import { ApiError, getAgentModels, getAgents, getProject, getSettings, type AgentCatalog, type AgentModelCatalog, type ProjectDetail, type SessionMode } from "../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../lib/connectionError";
import { chatErrorCopy, isChatPreflightError } from "../lib/chatError";
import { haptics } from "../lib/haptics";
import { resolveSpawnProject } from "../lib/projectFilter";
import { modelOverride, resolveSpawnAgent, resolveSpawnModel, spawnModelSourceChanged } from "../lib/spawnModel";
import { appendSpawnAttachments, readSpawnAttachments, type SpawnAttachment } from "../lib/spawn-attachments";
import { SpawnComposerControls } from "../lib/spawn-composer-controls";
import { SpawnPromptInput } from "../lib/spawn-prompt-input";
import { useApp } from "../lib/store";
import { useVoiceInput } from "../lib/voice/useVoiceInput";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Button } from "../lib/ui";
import { iconSize, space, type } from "../lib/tokens";
import { backOr } from "../lib/backNavigation";

export { SheetErrorBoundary as ErrorBoundary } from "../lib/RouteErrorBoundary";

export default function SpawnModal() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const { projectId: routeProjectId } = useLocalSearchParams<{ projectId?: string }>();
	const { projects, projectsKnown, activeProjectId, config, spawn } = useApp();

	const [projectId, setProjectId] = useState<string | null>(null);
	const [harness, setHarness] = useState("");
	const [agentTouched, setAgentTouched] = useState(false);
	const [mode, setMode] = useState<SessionMode>("chat");
	const [chatHarnesses, setChatHarnesses] = useState<string[]>([]);
	const [prompt, setPrompt] = useState("");
	const [attachments, setAttachments] = useState<SpawnAttachment[]>([]);
	const attachmentsRef = useRef<SpawnAttachment[]>([]);
	const pickingAttachments = useRef(false);
	const [attachmentError, setAttachmentError] = useState<string>();
	const [model, setModel] = useState("");
	const [modelTouched, setModelTouched] = useState(false);
	const [modelCatalog, setModelCatalog] = useState<AgentModelCatalog>();
	const [projectDetail, setProjectDetail] = useState<ProjectDetail>();
	const [projectDetailLoadedFor, setProjectDetailLoadedFor] = useState<string | null>(null);
	const [modelLoading, setModelLoading] = useState(false);
	const [modelError, setModelError] = useState<string>();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);

	const [catalog, setCatalog] = useState<AgentCatalog | null>(null);
	const [catalogError, setCatalogError] = useState<string | null>(null);
	const [loading, setLoading] = useState(true);
	const [offerTUI, setOfferTUI] = useState(false);
	// Spoken text lands in the prompt the way it does in the chat composer:
	// appended, so dictation can extend what was typed rather than replace it.
	const voice = useVoiceInput({ onTranscript: useCallback((spoken: string) => setPrompt((old) => old ? `${old} ${spoken}` : spoken), []) });
	const listening = voice.state === "starting" || voice.state === "recording";
	// iOS: the prompt fills the room above the controls. The controls translate
	// with the keyboard, which does not reflow their siblings; a spacer below
	// the attachments and messages makes that entire region reflow instead.
	const [promptRoom, setPromptRoom] = useState<number>();
	const keyboardHeight = useKeyboardState((state) => state.height);



	// Seed from the active project, or the only project. Mirrors the store's
	// `targetProject()`; kept here because the screen needs it as UI state to
	// drive the picker's value and the button's disabled state.
	useEffect(() => {
		const nextProjectId = resolveSpawnProject(
			projectId,
			routeProjectId,
			activeProjectId,
			projects,
			projectsKnown,
		);
		if (nextProjectId !== projectId) changeProject(nextProjectId);
	}, [activeProjectId, projects, projectsKnown, projectId, routeProjectId]);

	useEffect(() => {
		if (!config) return;
		let cancelled = false;
		setLoading(true);
		Promise.all([getAgents(config), getSettings(config)])
			.then(([c, settings]) => {
				if (cancelled) return;
				setCatalog(c);
				setChatHarnesses(settings.chatHarnesses);
				setCatalogError(null);
			})
			.catch((e) => {
				// Previously swallowed into `catalog = null`, which left an empty
				// picker and no way to tell the daemon was unreachable.
				if (!cancelled) setCatalogError(agentErrorCopy(e));
			})
			.finally(() => {
				if (!cancelled) setLoading(false);
			});
		return () => {
			cancelled = true;
		};
	}, [config]);

	// Refreshing the catalog moved into the agent sheet route, which owns its own
	// copy of it — see app/sheets/agent.tsx.
	const allAgents = useMemo(() => rankAgents(catalog), [catalog]);
	const agents = useMemo(() => mode === "chat" ? allAgents.filter((agent) => chatHarnesses.includes(agent.id)) : allAgents, [allAgents, chatHarnesses, mode]);
	const project = projects.find((item) => item.id === projectId);
	const projectWorkerAgent = projectDetail?.config?.worker?.agent ?? projectDetail?.agent ?? "";
	const projectWorkerModel = projectDetail?.config?.worker?.agentConfig?.model ?? projectDetail?.config?.agentConfig?.model ?? "";
	const resolvedModel = resolveSpawnModel({ selectedAgent: harness, projectWorkerAgent, projectWorkerModel });
	const displayedModel = modelTouched ? model : resolvedModel;
	// "Automatic" when the project pins nothing, because that is the truth: the
	// provider picks, and naming a model here promised one the session never ran.
	const displayedModelLabel = displayedModel ? modelCatalog?.models.find((item) => item.id === displayedModel)?.label ?? displayedModel : "Automatic";
	const modelSelection = modelTouched ? model : "__auto__";
	const hasComposerMessage = Boolean(
		(mode === "chat" && !loading && agents.length === 0)
		|| catalogError
		|| modelError
		|| attachmentError
		|| (Platform.OS === "android" && listening)
		|| voice.error
		|| error
		|| offerTUI,
	);

	useEffect(() => {
		if (!config || !projectId) { setProjectDetail(undefined); setProjectDetailLoadedFor(null); return; }
		let cancelled = false;
		setProjectDetailLoadedFor(null);
		getProject(config, projectId)
			.then((nextProject) => { if (!cancelled) setProjectDetail(nextProject); })
			.catch((cause) => { if (!cancelled) setModelError(cause instanceof Error ? cause.message : String(cause)); })
			.finally(() => { if (!cancelled) setProjectDetailLoadedFor(projectId); });
		return () => { cancelled = true; };
	}, [config, projectId]);

	useEffect(() => {
		if (agentTouched || loading || !catalog) return;
		if (projectId && projectDetailLoadedFor !== projectId) return;
		const nextHarness = resolveSpawnAgent({
			projectWorkerAgent: projectDetail?.config?.worker?.agent,
			projectAgent: projectDetail?.agent,
			availableAgents: agents.filter((agent) => agent.selectable).map((agent) => agent.id),
		});
		setHarness((current) => current === nextHarness ? current : nextHarness);
	}, [agentTouched, agents, catalog, loading, projectDetail, projectDetailLoadedFor, projectId]);

	useEffect(() => {
		if (!config || !projectId || !harness) { setModelCatalog(undefined); return; }
		let cancelled = false;
		setModelLoading(true);
		getAgentModels(config, harness, projectId)
			.then((nextCatalog) => { if (!cancelled) { setModelCatalog(nextCatalog); setModelError(nextCatalog.warning); } })
			.catch((cause) => { if (!cancelled) setModelError(cause instanceof Error ? cause.message : String(cause)); })
			.finally(() => { if (!cancelled) setModelLoading(false); });
		return () => { cancelled = true; };
	}, [config, harness, projectId]);

	const clearModelOverride = () => { setModel(""); setModelTouched(false); };
	const resetModelSource = () => { clearModelOverride(); setModelCatalog(undefined); setModelError(undefined); };
	const changeProject = (nextProjectId: string | null) => {
		if (!spawnModelSourceChanged({ projectId, agentId: harness }, { projectId: nextProjectId, agentId: harness })) return;
		resetModelSource();
		setProjectDetail(undefined);
		setProjectDetailLoadedFor(null);
		setAgentTouched(false);
		setProjectId(nextProjectId);
	};
	const selectAgent = (nextHarness: string) => {
		if (!spawnModelSourceChanged({ projectId, agentId: harness }, { projectId, agentId: nextHarness })) return;
		resetModelSource();
		setAgentTouched(true);
		setHarness(nextHarness);
	};
	const selectMode = (nextMode: SessionMode) => {
		if (nextMode === mode) return;
		const nextHarness = nextMode === "chat"
			? (chatHarnesses.includes(harness) ? harness : (defaultAgent(allAgents.filter((agent) => chatHarnesses.includes(agent.id))) ?? ""))
			: (harness || (defaultAgent(allAgents) ?? ""));
		if (spawnModelSourceChanged({ projectId, agentId: harness }, { projectId, agentId: nextHarness })) {
			resetModelSource();
			setAgentTouched(false);
		}
		setMode(nextMode);
		setHarness(nextHarness);
	};
	const selectModel = (nextModel: string) => {
		if (nextModel === "__auto__") {
			clearModelOverride();
			return;
		}
		setModel(nextModel);
		setModelTouched(true);
	};
	const voiceFeedback = listening ? (
		<View style={styles.voice}>
			<Feather name="mic" size={iconSize.xs} color={t.red} />
			<Text numberOfLines={2} style={styles.voiceText}>
				{voice.partial || (voice.state === "starting" ? "Keep holding…" : "Listening…")}
			</Text>
		</View>
	) : null;
	const pickAttachments = async () => {
		if (pickingAttachments.current) return;
		pickingAttachments.current = true;
		setAttachmentError(undefined);
		try {
			const result = await DocumentPicker.getDocumentAsync({
				multiple: true,
				copyToCacheDirectory: true,
				type: "*/*",
			});
			if (result.canceled) return;
			const picked = result.assets.map((asset) => {
				const file = new File(asset.uri);
				return {
					name: asset.name,
					mimeType: asset.mimeType || "application/octet-stream",
					bytes: asset.size ?? file.size,
					readData: () => file.base64(),
				};
			});
			const before = attachmentsRef.current;
			const next = await readSpawnAttachments(before, picked);
			const merged = appendSpawnAttachments(attachmentsRef.current, next.attachments.slice(before.length));
			attachmentsRef.current = merged.attachments;
			setAttachments(merged.attachments);
			setAttachmentError(next.error ?? merged.error);
		} catch (cause) {
			setAttachmentError(cause instanceof Error ? cause.message : "Couldn't read that file.");
		} finally {
			pickingAttachments.current = false;

		}
	};

	const onSpawn = async () => {
		if (pickingAttachments.current) {
			setAttachmentError("Wait for attachments to finish loading.");
			return;
		}
		// Validated on submit rather than by disabling the button — desktop's
		// choice, and the better one: a disabled button with no explanation is
		// worse than a message naming what is missing.
		setBusy(true);
		setError(null);
		setOfferTUI(false);
		try {
			const session = await spawn({
				projectId: projectId ?? undefined,
				prompt: prompt.trim() || undefined,
				harness: harness || undefined,
				model: modelOverride(displayedModel, modelTouched),
				mode,
				attachments: attachmentsRef.current.map(({ mimeType, data }) => ({ mimeType, data })),
			});
			haptics.success();
			// Dismiss the modal first, then open the freshly spawned session's mode-aware surface
			// once the dismiss transition has settled. Firing both navigations in the
			// same tick overlaps their animations (the modal retracts while the session
			// is already sliding in); runAfterInteractions waits for the modal's
			// transition to finish so the two happen back-to-back, not on top of each
			// other. The session screen shows its own "connecting" state while the
			// terminal attaches, so landing on it before the PTY is ready is expected.
			backOr(router);
			InteractionManager.runAfterInteractions(() => {
				router.push({
					pathname: "/session/[id]",
					params: { id: session.id, projectId: session.projectId },
				});
			});
		} catch (e) {
			haptics.error();
			setError(spawnErrorCopy(e));
			setOfferTUI(mode === "chat" && isChatPreflightError(e));
			setBusy(false);
		}
	};

	const content = (
		<View style={[
			styles.content,
			Platform.OS === "ios" && styles.iosContent,
			Platform.OS === "android" && styles.androidContent,
		]}>
				<View
					style={[styles.promptHost, Platform.OS === "ios" && styles.promptHostFill]}
					onLayout={Platform.OS === "ios" ? (event) => setPromptRoom(Math.floor(event.nativeEvent.layout.height)) : undefined}
				>
					<SpawnPromptInput value={prompt} onChangeText={setPrompt} height={Platform.OS === "ios" ? promptRoom : undefined} />
				</View>

				{attachments.length ? (
					<ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.attachments}>
						{attachments.map((item, index) => (
							<View key={`${item.name}-${index}`} style={styles.attachment}>
								<Feather name="file-text" size={iconSize.sm} color={t.accent} />
								<Text numberOfLines={1} style={styles.attachmentName}>{item.name}</Text>
								<Pressable
									hitSlop={8}
									accessibilityLabel={`Remove ${item.name}`}
									onPress={() => {
										attachmentsRef.current = attachmentsRef.current.filter((candidate) => candidate !== item);
										setAttachments(attachmentsRef.current);
									}}
								>
									<Feather name="x" size={iconSize.xs} color={t.textTertiary} />
								</Pressable>
							</View>
						))}
					</ScrollView>
				) : null}

		{hasComposerMessage ? <View style={styles.messages}>
					{mode === "chat" && !loading && agents.length === 0 ? <Text style={styles.warn}>No installed agent on this AO host currently supports Chat. Choose Terminal UI or install/authenticate a Chat-capable agent.</Text> : null}
					{catalogError ? <Text style={styles.warn}>{catalogError}</Text> : null}
					{modelError ? <Text style={styles.warn}>{modelError}</Text> : null}
					{attachmentError ? <Text style={styles.warn}>{attachmentError}</Text> : null}
					{Platform.OS === "android" ? voiceFeedback : null}
					{voice.error ? <Text accessibilityRole="alert" style={styles.warn}>{voice.error}</Text> : null}
					{error ? <Text style={styles.error}>{error}</Text> : null}
					{offerTUI ? <Button title="Create as Terminal UI instead" variant="ghost" icon="terminal" onPress={() => { selectMode("tui"); setOfferTUI(false); setError(null); }} /> : null}
				</View> : null}

				{/* The sticky controls move visually but keep their original layout
				    position. Reserve that movement before them so chips and messages
				    remain visible above the keyboard, not behind the controls. */}
				{Platform.OS === "ios" && keyboardHeight > 0 ? (
					<View pointerEvents="none" style={{ height: keyboardHeight, marginTop: -space.sm }} />
				) : null}

				{/* The controls ride the keyboard on the UI thread.
				    iOS does not lift this form sheet for the IME, and every
				    height-based attempt moved late or not at all: a settled keyboard
				    height only lands after the animation, animated padding is
				    interpolated on the JS thread, and a keyboard-avoiding wrapper
				    mismeasures its own frame inside a sheet, leaving Start task behind
				    the keyboard. A sticky view translates by the live offset, so
				    the selectors and the button sit directly above it. */}
				<KeyboardStickyView offset={{ closed: 0, opened: 0 }}>
				{Platform.OS === "ios" ? voiceFeedback : null}
				<SpawnComposerControls
					projects={projects.map((item) => ({ id: item.id, label: item.name }))}
					projectId={project?.id ?? null}
					onSelectProject={changeProject}
					agents={agents.filter((item) => item.selectable).map((item) => ({ id: item.id, label: item.label }))}
					harness={harness}
					onSelectHarness={selectAgent}
					models={modelCatalog?.models.map((item) => ({ id: item.id, label: item.label })) ?? []}
					modelSelection={modelSelection}
					modelLabel={displayedModelLabel}
					onSelectModel={selectModel}
					onAttach={() => { void pickAttachments(); }}
					voice={{ state: voice.state, mode: voice.mode, onPressIn: voice.pressIn, onPressOut: voice.pressOut }}
					onSpawn={() => { void onSpawn(); }}
					busy={busy}
					disabled={!projectId || !harness || busy || modelLoading || loading || listening || voice.state === "transcribing"}
				/>
				</KeyboardStickyView>
		</View>
	);

	if (Platform.OS === "android") {
		return (
			<View style={styles.androidModalRoot}>
				<BottomSheet
					index={0}
					enablePanDownToClose
					enableDynamicSizing
					backgroundStyle={{ backgroundColor: t.bgBase }}
					onClose={() => backOr(router)}
				>
					<BottomSheetView style={styles.androidSheet}>
						{content}
					</BottomSheetView>
				</BottomSheet>
			</View>
		);
	}

	return <View style={styles.screen}>{content}</View>;
}

// Human copy for a failed spawn, matching every other screen. This one used to
// render `e.message` — the wire string, e.g. "401 - missing or invalid
// connection password".
function spawnErrorCopy(e: unknown): string {
	if (isChatPreflightError(e)) return chatErrorCopy(e);
	if (e instanceof ApiError && e.code === "PROMPT_TOO_LONG") {
		return "Task prompt is too long. Keep it to 16 KiB or fewer (emoji and other non-English characters use more than one byte). Shorten it and try again.";
	}
	const status = e instanceof ApiError ? e.status : undefined;
	const { title, message } = describeConnectionFailure(classifyConnectionFailure(status), {
		host: "",
		port: "",
		platform: Platform.OS,
	});
	return `${title} ${message}`;
}

// Android's compact field height and the iOS host's minimum layout height.
const PROMPT_MIN_HEIGHT = 112;

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		content: { flex: 1, paddingHorizontal: space.lg, paddingTop: space.lg, paddingBottom: space.sm, gap: space.sm },
		iosContent: { paddingTop: space.xxxl },
		androidModalRoot: { flex: 1, backgroundColor: "transparent" },
		androidSheet: {
			paddingTop: space.xs,
			paddingBottom: space.md,
			backgroundColor: t.bgBase,
		},
		androidContent: { flex: 0, paddingTop: space.md, paddingBottom: space.none },
		messages: { gap: space.xs },
		voice: { flexDirection: "row", alignItems: "center", gap: space.xs, backgroundColor: t.tintRed, borderRadius: 8, paddingHorizontal: space.sm, paddingVertical: space.xs },
		voiceText: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize },
		promptHost: { width: "100%", height: PROMPT_MIN_HEIGHT },
		promptHostFill: { height: undefined, flex: 1, minHeight: 0 },
		attachments: { gap: space.sm },
		attachment: { maxWidth: 190, height: 36, flexDirection: "row", alignItems: "center", gap: space.xs, paddingHorizontal: space.sm, borderRadius: 12, borderCurve: "continuous", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderSubtle },
		attachmentName: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textSecondary, fontSize: type.caption1.fontSize },
		warn: { fontFamily: "Geist_400Regular", color: t.amber, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
		error: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
	});
