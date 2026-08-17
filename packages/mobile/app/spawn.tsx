import { useRouter } from "expo-router";
import { Feather } from "@expo/vector-icons";
import { useEffect, useMemo, useState } from "react";
import {
	InteractionManager,
	Keyboard,
	KeyboardAvoidingView,
	LayoutAnimation,
	Platform,
	Pressable,
	ScrollView,
	StyleSheet,
	Text,
	TextInput,
	View,
} from "react-native";
import { AgentLogo } from "../lib/AgentLogo";
import { agentErrorCopy } from "../lib/agentError";
import { defaultAgent, rankAgents } from "../lib/agentPicker";
import { ApiError, getAgentModels, getAgents, getProject, getSettings, type AgentCatalog, type AgentModelCatalog, type ProjectDetail, type SessionMode } from "../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../lib/connectionError";
import { chatErrorCopy, isChatPreflightError } from "../lib/chatError";
import { haptics } from "../lib/haptics";
import { agentSheetRoute, modelSheetRoute, projectSheetRoute } from "../lib/sheetResult";
import { screenKeyboardAvoidance } from "../lib/session/keyboardInset";
import { modelOverride, resolveSpawnAgent, resolveSpawnModel, spawnModelSourceChanged } from "../lib/spawnModel";
import { useApp } from "../lib/store";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Button, SettingsGroup, SettingsRow } from "../lib/ui";

export default function SpawnModal() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const { projects, activeProjectId, config, spawn } = useApp();

	const [projectId, setProjectId] = useState<string | null>(null);
	const [harness, setHarness] = useState("");
	const [agentTouched, setAgentTouched] = useState(false);
	const [mode, setMode] = useState<SessionMode>("chat");
	const [chatHarnesses, setChatHarnesses] = useState<string[]>([]);
	const [prompt, setPrompt] = useState("");
	const [model, setModel] = useState("");
	const [modelTouched, setModelTouched] = useState(false);
	const [modelCatalog, setModelCatalog] = useState<AgentModelCatalog>();
	const [projectDetail, setProjectDetail] = useState<ProjectDetail>();
	const [projectDetailLoadedFor, setProjectDetailLoadedFor] = useState<string | null>(null);
	const [modelLoading, setModelLoading] = useState(false);
	const [modelError, setModelError] = useState<string>();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [keyboardHeight, setKeyboardHeight] = useState(0);

	const [catalog, setCatalog] = useState<AgentCatalog | null>(null);
	const [catalogError, setCatalogError] = useState<string | null>(null);
	const [loading, setLoading] = useState(true);
	const [offerTUI, setOfferTUI] = useState(false);

	useEffect(() => {
		if (Platform.OS !== "android") return;
		const avoidance = screenKeyboardAvoidance("android", 0);
		const animate = (duration?: number) => LayoutAnimation.configureNext({
			duration: duration || 250,
			update: { type: LayoutAnimation.Types.keyboard },
		});
		const show = Keyboard.addListener(avoidance.showEvent, (event) => {
			animate(event.duration);
			setKeyboardHeight(event.endCoordinates.height);
		});
		const hide = Keyboard.addListener(avoidance.hideEvent, (event) => {
			animate(event?.duration);
			setKeyboardHeight(0);
		});
		return () => {
			show.remove();
			hide.remove();
		};
	}, []);

	// Seed from the active project, or the only project. Mirrors the store's
	// `targetProject()`; kept here because the screen needs it as UI state to
	// drive the picker's value and the button's disabled state.
	useEffect(() => {
		if (projectId) return;
		if (activeProjectId !== "all") setProjectId(activeProjectId);
		else if (projects.length === 1) setProjectId(projects[0].id);
	}, [activeProjectId, projects, projectId]);

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
	const selectedAgent = agents.find((a) => a.id === harness);
	const project = projects.find((p) => p.id === projectId);
	const projectWorkerAgent = projectDetail?.config?.worker?.agent ?? projectDetail?.agent ?? "";
	const projectWorkerModel = projectDetail?.config?.worker?.agentConfig?.model ?? projectDetail?.config?.agentConfig?.model ?? "";
	const catalogDefault = modelCatalog?.models.find((item) => item.isDefault)?.id ?? "";
	const resolvedModel = resolveSpawnModel({ selectedAgent: harness, projectWorkerAgent, projectWorkerModel, catalogDefault });
	const displayedModel = modelTouched ? model : resolvedModel;
	const displayedModelLabel = displayedModel ? modelCatalog?.models.find((item) => item.id === displayedModel)?.label ?? displayedModel : "Auto";

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
	const selectProject = (nextProjectId: string) => {
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

	return (
		<KeyboardAvoidingView
			style={[
				styles.screen,
				Platform.OS === "android" && keyboardHeight > 0
					? screenKeyboardAvoidance("android", keyboardHeight).rootStyle
					: undefined,
			]}
			behavior={Platform.OS === "ios" ? "padding" : undefined}
		>
			<ScrollView contentContainerStyle={{ padding: 16, paddingBottom: 40 }} keyboardShouldPersistTaps="handled">
				<Text style={styles.lead}>
					Spawn a worker agent. It gets its own isolated workspace, then starts on the task you give it.
				</Text>

				<SettingsGroup footer="Agent availability is cached.">
					<SettingsRow
						icon="folder"
						label="Project"
						value={project?.name ?? "Choose a project"}
						onPress={() =>
							router.push(
								projectSheetRoute({
									selected: projectId ?? "",
								onSelect: selectProject,
									// "All projects" is a filter — there is nothing to spawn into.
									includeAll: false,
									title: "Project",
									subtitle: "Where this agent gets its workspace.",
								}),
							)
						}
					/>
					<SettingsRow
						icon="cpu"
						label="Agent"
						value={loading ? "Loading…" : (selectedAgent?.label ?? "Choose an agent")}
						// The collapsed row carries the mark too, as desktop's trigger does.
						leading={selectedAgent ? <AgentLogo harness={selectedAgent.id} size={20} /> : undefined}
						disabled={loading}
					onPress={() => router.push(agentSheetRoute({ selected: harness, onSelect: selectAgent, allowed: mode === "chat" ? chatHarnesses : undefined, mode }))}
				/>
				<SettingsRow
					icon="box"
					label="Model"
					value={modelLoading ? "Loading…" : displayedModelLabel}
					disabled={!projectId || !harness || modelLoading}
					onPress={() => projectId && harness && router.push(modelSheetRoute({ agentId: harness, projectId, selected: displayedModel, onSelect: (value) => { setModel(value); setModelTouched(true); } }))}
				/>
			</SettingsGroup>

				<Text style={styles.label}>INTERFACE</Text>
				<View accessibilityRole="radiogroup" style={styles.modeControl}>
					<ModeChoice
						label="Chat"
						detail="Native conversation"
						selected={mode === "chat"}
							onPress={() => selectMode("chat")}
						/>
					<ModeChoice label="Terminal UI" detail="Agent's own TUI" selected={mode === "tui"} onPress={() => selectMode("tui")} />
				</View>
				<Text style={styles.hint}>{mode === "chat" ? "Chat is the mobile default. The agent runs through its structured controller; no tmux is created for it." : "Compatibility mode. The agent runs inside tmux and mobile mirrors its terminal."}</Text>
				{mode === "chat" && !loading && agents.length === 0 ? <Text style={styles.warn}>No installed agent on this AO host currently supports Chat. Choose Terminal UI or install/authenticate a Chat-capable agent.</Text> : null}

				{catalogError ? <Text style={styles.warn}>{catalogError}</Text> : null}

				<Text style={styles.label}>TASK (OPTIONAL)</Text>
				<TextInput
					style={[styles.input, styles.textarea]}
					value={prompt}
					onChangeText={setPrompt}
					placeholder="Describe the task (optional)…"
					placeholderTextColor={t.textFaint}
					multiline
					autoCapitalize="sentences"
				/>
				{modelError ? <Text style={styles.warn}>{modelError}</Text> : null}

				{error ? <Text style={styles.error}>{error}</Text> : null}
				{offerTUI ? <Button title="Create as Terminal UI instead" variant="ghost" icon="terminal" onPress={() => { selectMode("tui"); setOfferTUI(false); setError(null); }} style={{ marginTop: 12 }} /> : null}

				<Button
					title="Spawn agent"
					icon="zap"
					loading={busy}
					onPress={onSpawn}
					disabled={!projectId || !harness}
					style={{ marginTop: 20 }}
				/>
				<Button title="Cancel" variant="ghost" onPress={() => router.back()} style={{ marginTop: 10 }} />
			</ScrollView>

		</KeyboardAvoidingView>
	);
}

function ModeChoice({ label, detail, selected, onPress }: { label: string; detail: string; selected: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			accessibilityRole="radio"
			accessibilityState={{ selected }}
			onPress={() => { haptics.select(); onPress(); }}
			style={({ pressed }) => [styles.modeChoice, selected && styles.modeChoiceSelected, pressed && { opacity: 0.75 }]}
		>
			<Feather name={label === "Chat" ? "message-square" : "terminal"} size={16} color={selected ? t.blue : t.textTertiary} />
			<View style={{ flex: 1 }}><Text style={[styles.modeLabel, selected && { color: t.blue }]}>{label}</Text><Text style={styles.modeDetail}>{detail}</Text></View>
			{selected ? <Feather name="check" size={15} color={t.blue} /> : null}
		</Pressable>
	);
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
		lead: { color: t.textSecondary, fontSize: 14, lineHeight: 20, marginBottom: 22 },
		label: {
			color: t.textTertiary,
			fontSize: 11,
			letterSpacing: 1.2,
			fontWeight: "700",
			marginTop: 18,
			marginBottom: 8,
			marginLeft: 4,
		},
		input: {
			backgroundColor: t.bgElevated,
			borderColor: t.borderSubtle,
			borderWidth: 1,
			borderRadius: 12,
			color: t.textPrimary,
			paddingHorizontal: 14,
			paddingVertical: 12,
			fontSize: 15,
		},
		textarea: { minHeight: 96, textAlignVertical: "top" },
		hint: { color: t.textTertiary, fontSize: 12, lineHeight: 17, marginTop: 8, marginHorizontal: 4 },
		warn: { color: t.amber, fontSize: 13, lineHeight: 18, marginTop: 4 },
		error: { color: t.red, fontSize: 13, lineHeight: 18, marginTop: 16 },
		modeControl: { flexDirection: "row", gap: 9 },
		modeChoice: { flex: 1, minHeight: 64, flexDirection: "row", alignItems: "center", gap: 8, padding: 10, borderRadius: 12, borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgElevated },
		modeChoiceSelected: { borderColor: t.blue, backgroundColor: t.tintBlue },
		modeLabel: { color: t.textPrimary, fontSize: 13, fontWeight: "700" },
		modeDetail: { color: t.textTertiary, fontSize: 9, marginTop: 2 },
	});
