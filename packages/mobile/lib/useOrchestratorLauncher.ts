import { useRouter } from "expo-router";
import { useCallback, useRef, useState } from "react";
import { Alert, Platform } from "react-native";
import { ApiError } from "./api";
import { chatErrorCopy, isChatPreflightError } from "./chatError";
import { classifyConnectionFailure, describeConnectionFailure } from "./connectionError";
import { haptics } from "./haptics";
import type { OrchestratorProjectRow } from "./orchestratorView";
import { hostedRowKey } from "./hostedRows";
import { useApp } from "./store";

function rowHostId(row: OrchestratorProjectRow, fallback?: string): string {
	return "hostId" in row.project && typeof row.project.hostId === "string" ? row.project.hostId : fallback ?? "";
}

/**
 * Opening, starting and resuming a project's orchestrator.
 *
 * Lifted out of the Projects screen so the project card and the project page
 * run exactly the same launch: the chat-preflight fallback to Terminal UI, the
 * connection error copy, and the guard against a second tap starting a second
 * orchestrator. Two copies of that would drift the first time either changed.
 */
export function useOrchestratorLauncher() {
	const router = useRouter();
	const { currentHostId, configForHost, refreshHost, launchConductor } = useApp();
	const [busyProjects, setBusyProjects] = useState<ReadonlySet<string>>(() => new Set());
	// A ref as well as state: state is a render behind, and a fast double tap must
	// not slip a second launch in before the first one has re-rendered.
	const launching = useRef(new Set<string>());

	const setBusy = useCallback((projectId: string, busy: boolean) => {
		setBusyProjects((current) => {
			const next = new Set(current);
			if (busy) next.add(projectId);
			else next.delete(projectId);
			return next;
		});
	}, []);

	const openSession = useCallback((row: OrchestratorProjectRow, id: string) => {
		router.push({ pathname: "/session/[id]", params: { id, projectId: row.project.id, hostId: rowHostId(row, currentHostId) } });
	}, [currentHostId, router]);

	const runLaunch = useCallback(async (row: OrchestratorProjectRow, mode: "chat" | "tui" = "chat") => {
		const hostId = rowHostId(row, currentHostId);
		const key = hostedRowKey(hostId, row.project.id);
		if (launching.current.has(key)) return;
		launching.current.add(key);
		setBusy(key, true);
		try {
			const next = await launchConductor(row.project.id, false, mode, hostId);
			if (next?.id) openSession(row, next.id);
			else await refreshHost(hostId);
		} catch (cause) {
			haptics.error();
			if (mode === "chat" && isChatPreflightError(cause)) {
				Alert.alert("Chat is unavailable", chatErrorCopy(cause), [
					{ text: "Cancel", style: "cancel" },
					{ text: "Start Terminal UI", onPress: () => void runLaunch(row, "tui") },
				]);
				return;
			}
			const httpStatus = cause instanceof ApiError ? cause.status : undefined;
			const target = configForHost(hostId);
			const copy = describeConnectionFailure(classifyConnectionFailure(httpStatus), {
				host: target?.host ?? "",
				port: target?.httpPort ?? "",
				platform: Platform.OS,
			});
			Alert.alert(copy.title, copy.message);
		} finally {
			launching.current.delete(key);
			setBusy(key, false);
		}
	}, [currentHostId, configForHost, launchConductor, openSession, refreshHost, setBusy]);

	/** Opens a running orchestrator, or starts or resumes one that is not. */
	const openOrchestrator = useCallback((row: OrchestratorProjectRow) => {
		if (row.action === "open" && row.link?.id) {
			haptics.select();
			openSession(row, row.link.id);
			return;
		}
		haptics.tap();
		void runLaunch(row);
	}, [openSession, runLaunch]);

	return { busyProjects, openOrchestrator, openSession };
}
