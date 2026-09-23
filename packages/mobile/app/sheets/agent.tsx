import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect, useMemo, useState } from "react";
import { AgentPickerSheet } from "../../lib/AgentPickerSheet";
import { agentErrorCopy } from "../../lib/agentError";
import { rankAgents } from "../../lib/agentPicker";
import { getAgents, refreshAgents } from "../../lib/api";
import { haptics } from "../../lib/haptics";
import { releaseSheetResult, takeSheetResult } from "../../lib/sheetResult";
import { useApp } from "../../lib/store";
import { backOr } from "../../lib/backNavigation";

// The agent picker, as a native form sheet.
//
// It owns its catalog rather than receiving one from the spawn screen: a route
// can't be handed props, and fetching on open is the better behaviour anyway —
// the list is current every time it's shown. The spawn screen keeps its own copy
// only to label the trigger row and seed the default.
export default function AgentSheetRoute() {
	const router = useRouter();
	const { config } = useApp();
	const { resultKey, selected, allowed, mode } = useLocalSearchParams<{ resultKey?: string; selected?: string; allowed?: string; mode?: string }>();

	const [catalog, setCatalog] = useState<Awaited<ReturnType<typeof getAgents>> | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [refreshing, setRefreshing] = useState(false);

	useEffect(() => () => releaseSheetResult(resultKey), [resultKey]);

	useEffect(() => {
		if (!config) return;
		let cancelled = false;
		getAgents(config)
			.then((c) => {
				if (cancelled) return;
				setCatalog(c);
				setError(null);
			})
			.catch((e) => {
				if (!cancelled) setError(agentErrorCopy(e));
			});
		return () => {
			cancelled = true;
		};
	}, [config]);

	async function onRefresh() {
		if (!config || refreshing) return;
		setRefreshing(true);
		try {
			setCatalog(await refreshAgents(config));
			setError(null);
			haptics.success();
		} catch (e) {
			haptics.error();
			setError(agentErrorCopy(e));
		} finally {
			setRefreshing(false);
		}
	}

	const allowedSet = useMemo(() => allowed ? new Set(allowed.split("|").filter(Boolean)) : undefined, [allowed]);
	const agents = useMemo(() => rankAgents(catalog).filter((agent) => !allowedSet || allowedSet.has(agent.id)), [catalog, allowedSet]);

	return (
		<AgentPickerSheet
			agents={agents}
			selected={selected ?? ""}
			error={error}
			refreshing={refreshing}
			onRefresh={onRefresh}
			onClose={() => backOr(router)}
			onSelect={(id) => takeSheetResult<string>(resultKey)?.(id)}
			title={mode === "chat" ? "Chat agent" : "Agent"}
			subtitle={mode === "chat" ? "Installed agents with a structured Chat controller." : "The CLI that runs this terminal session."}
		/>
	);
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
