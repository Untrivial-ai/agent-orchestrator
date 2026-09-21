import { useLocalSearchParams, useRouter } from "expo-router";
import { Feather } from "@expo/vector-icons";
import BottomSheet, { BottomSheetView } from "@expo/ui/community/bottom-sheet";
import * as DocumentPicker from "expo-document-picker";
import { File } from "expo-file-system";
import { useEffect, useMemo, useState } from "react";
import {
	InteractionManager,
	Platform,
	Pressable,
	ScrollView,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { KeyboardStickyView } from "react-native-keyboard-controller";
import { agentErrorCopy } from "../lib/agentError";
import { defaultAgent, rankAgents } from "../lib/agentPicker";
import { ApiError, getAgentModels, getAgents, getProject, getSettings, type AgentCatalog, type AgentModelCatalog, type ProjectDetail, type SessionMode } from "../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../lib/connectionError";
import { chatErrorCopy, isChatPreflightError } from "../lib/chatError";
import { haptics } from "../lib/haptics";
import { resolveSpawnProject } from "../lib/projectFilter";
import { modelOverride, resolveSpawnAgent, resolveSpawnModel, spawnModelSourceChanged } from "../lib/spawnModel";
import { appendSpawnAttachments, type SpawnAttachment } from "../lib/spawn-attachments";
import { SpawnComposerControls } from "../lib/spawn-composer-controls";
import { SpawnPromptInput } from "../lib/spawn-prompt-input";
import { useApp } from "../lib/store";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Button } from "../lib/ui";

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
	const catalogDefault = modelCatalog?.models.find((item) => item.isDefault)?.id ?? "";
	const resolvedModel = resolveSpawnModel({ selectedAgent: harness, projectWorkerAgent, projectWorkerModel, catalogDefault });
	const displayedModel = modelTouched ? model : resolvedModel;
	const displayedModelLabel = displayedModel ? modelCatalog?.models.find((item) => item.id === displayedModel)?.label ?? displayedModel : "Auto";
	const modelSelection = modelTouched ? model : "__auto__";
	const hasComposerMessage = Boolean(
		(mode === "chat" && !loading && agents.length === 0)
		|| catalogError
		|| modelError
		|| attachmentError
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
	const pickAttachments = async () => {
		setAttachmentError(undefined);
		try {
			const result = await DocumentPicker.getDocumentAsync({
				multiple: true,
				copyToCacheDirectory: true,
				type: "*/*",
			});
			if (result.canceled) return;
			const picked: SpawnAttachment[] = [];
			for (const asset of result.assets) {
				const file = new File(asset.uri);
				const bytes = asset.size ?? file.size ?? 0;
				// Avoid reading an oversized file into JS memory merely to reject it.
				if (bytes > 10 * 1024 * 1024) {
					picked.push({ name: asset.name, mimeType: asset.mimeType || "application/octet-stream", data: "", bytes });
					continue;
				}
				picked.push({
					name: asset.name,
					mimeType: asset.mimeType || "application/octet-stream",
					data: await file.base64(),
					bytes,
				});
			}
			const next = appendSpawnAttachments(attachments, picked);
			setAttachments(next.attachments);
			setAttachmentError(next.error);
		} catch (cause) {
			setAttachmentError(cause instanceof Error ? cause.message : "Could not read the selected file.");
		}
	};

	const onSpawn = async () => {
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
				model: modelOverride(displayedModel, resolvedModel, modelTouched),
				mode,
				attachments: attachments.map(({ mimeType, data }) => ({ mimeType, data })),
			});
			haptics.success();
			// Dismiss the modal first, then open the freshly spawned session's mode-aware surface
			// once the dismiss transition has settled. Firing both navigations in the
			// same tick overlaps their animations (the modal retracts while the session
			// is already sliding in); runAfterInteractions waits for the modal's
			// transition to finish so the two happen back-to-back, not on top of each
			// other. The session screen shows its own "connecting" state while the
			// terminal attaches, so landing on it before the PTY is ready is expected.
			router.back();
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
		<View style={[styles.content, Platform.OS === "android" && styles.androidContent]}>
				<View style={styles.promptHost}>
					<SpawnPromptInput value={prompt} onChangeText={setPrompt} />
				</View>

				{attachments.length ? (
					<ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.attachments}>
						{attachments.map((item, index) => (
							<View key={`${item.name}-${index}`} style={styles.attachment}>
								<Feather name="file-text" size={14} color={t.blue} />
								<Text numberOfLines={1} style={styles.attachmentName}>{item.name}</Text>
								<Pressable
									hitSlop={8}
									accessibilityLabel={`Remove ${item.name}`}
									onPress={() => setAttachments((current) => current.filter((candidate) => candidate !== item))}
								>
									<Feather name="x" size={13} color={t.textTertiary} />
								</Pressable>
							</View>
						))}
					</ScrollView>
				) : null}

		{Platform.OS === "ios" ? <View style={styles.flexSpacer} /> : null}

		{hasComposerMessage ? <View style={styles.messages}>
					{mode === "chat" && !loading && agents.length === 0 ? <Text style={styles.warn}>No installed agent on this AO host currently supports Chat. Choose Terminal UI or install/authenticate a Chat-capable agent.</Text> : null}
					{catalogError ? <Text style={styles.warn}>{catalogError}</Text> : null}
					{modelError ? <Text style={styles.warn}>{modelError}</Text> : null}
					{attachmentError ? <Text style={styles.warn}>{attachmentError}</Text> : null}
					{error ? <Text style={styles.error}>{error}</Text> : null}
					{offerTUI ? <Button title="Create as Terminal UI instead" variant="ghost" icon="terminal" onPress={() => { selectMode("tui"); setOfferTUI(false); setError(null); }} /> : null}
				</View> : null}

				{/* The controls ride the keyboard on the UI thread.
				    iOS does not lift this form sheet for the IME, and every
				    height-based attempt moved late or not at all: a settled keyboard
				    height only lands after the animation, animated padding is
				    interpolated on the JS thread, and a keyboard-avoiding wrapper
				    mismeasures its own frame inside a sheet, leaving Start task behind
				    the keyboard. A sticky view translates by the live offset, so
				    the selectors and the button sit directly above it. */}
				<KeyboardStickyView offset={{ closed: 0, opened: 0 }}>
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
					onSpawn={() => { void onSpawn(); }}
					busy={busy}
					disabled={!projectId || !harness || busy || modelLoading || loading}
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
					onClose={() => router.back()}
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
	const status = e instanceof ApiError ? e.status : undefined;
	const { title, message } = describeConnectionFailure(classifyConnectionFailure(status), {
		host: "",
		port: "",
		platform: Platform.OS,
	});
	return `${title} ${message}`;
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		content: { flex: 1, paddingHorizontal: 18, paddingTop: 18, paddingBottom: 8, gap: 10 },
		androidModalRoot: { flex: 1, backgroundColor: "transparent" },
		androidSheet: {
			paddingTop: 6,
			paddingBottom: 12,
			backgroundColor: t.bgBase,
		},
		androidContent: { flex: 0, paddingTop: 12, paddingBottom: 0 },
		flexSpacer: { flex: 1 },
		messages: { gap: 6 },
		promptHost: { width: "100%", height: 112 },
		attachments: { gap: 8 },
		attachment: { maxWidth: 190, height: 36, flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 10, borderRadius: 12, borderCurve: "continuous", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderSubtle },
		attachmentName: { flexShrink: 1, color: t.textSecondary, fontSize: 12 },
		warn: { color: t.amber, fontSize: 13, lineHeight: 18 },
		error: { color: t.red, fontSize: 13, lineHeight: 18 },
	});
