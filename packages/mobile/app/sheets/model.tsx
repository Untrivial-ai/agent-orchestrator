import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect, useState } from "react";
import { getAgentModels, refreshAgentModels, type AgentModelCatalog } from "../../lib/api";
import { ModelPickerSheet } from "../../lib/ModelPickerSheet";
import { releaseSheetResult, takeSheetResult } from "../../lib/sheetResult";
import { useApp } from "../../lib/store";

export default function ModelSheetRoute() {
	const router = useRouter();
	const { config } = useApp();
	const { resultKey, agentId = "", projectId = "", selected = "" } = useLocalSearchParams<{ resultKey?: string; agentId?: string; projectId?: string; selected?: string }>();
	const [catalog, setCatalog] = useState<AgentModelCatalog>();
	const [error, setError] = useState<string>();
	const [loading, setLoading] = useState(true);
	const [refreshing, setRefreshing] = useState(false);
	useEffect(() => () => releaseSheetResult(resultKey), [resultKey]);
	useEffect(() => { if (!config || !agentId) return; let cancelled = false; getAgentModels(config, agentId, projectId).then((value) => { if (!cancelled) { setCatalog(value); setError(undefined); } }).catch((cause) => { if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause)); }).finally(() => { if (!cancelled) setLoading(false); }); return () => { cancelled = true; }; }, [agentId, config, projectId]);
	return <ModelPickerSheet catalog={catalog} selected={selected} loading={loading} refreshing={refreshing} error={error} onClose={() => router.back()} onSelect={(value) => takeSheetResult<string>(resultKey)?.(value)} onRefresh={() => { if (!config || !agentId || refreshing) return; setRefreshing(true); refreshAgentModels(config, agentId, projectId).then((value) => { setCatalog(value); setError(undefined); }).catch((cause) => setError(cause instanceof Error ? cause.message : String(cause))).finally(() => setRefreshing(false)); }} />;
}
