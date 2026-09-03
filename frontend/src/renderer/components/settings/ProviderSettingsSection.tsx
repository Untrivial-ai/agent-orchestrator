import { useCallback, useEffect, useState } from "react";
import { apiClient } from "../../lib/api-client";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { SettingsSection } from "./SettingsSection";

type Provider = { id: string; displayName: string; apiProtocol: string; baseUrl: string; enabled: boolean; secretConfigured: boolean };
type ProviderModel = { id: string; providerId: string; displayName: string; modelName: string; enabled: boolean; sortOrder: number };

export function ProviderSettingsSection({ titleHidden = false }: { titleHidden?: boolean }) {
	const [providers, setProviders] = useState<Provider[]>([]);
	const [models, setModels] = useState<Record<string, ProviderModel[]>>({});
	const [editingProviderId, setEditingProviderId] = useState("");
	const [editingModelId, setEditingModelId] = useState("");
	const [displayName, setDisplayName] = useState("");
	const [baseUrl, setBaseUrl] = useState("");
	const [apiKey, setApiKey] = useState("");
	const [modelProviderId, setModelProviderId] = useState("");
	const [modelDisplayName, setModelDisplayName] = useState("");
	const [modelName, setModelName] = useState("");
	const [message, setMessage] = useState("");
	const load = useCallback(async () => {
		const { data: body, error } = await apiClient.GET("/api/v1/providers");
		if (error) throw new Error("Unable to load providers");
		setProviders(body.providers ?? []);
		const details = await Promise.all((body.providers ?? []).map(async (provider) => {
			const { data } = await apiClient.GET("/api/v1/providers/{providerId}", { params: { path: { providerId: provider.id } } });
			return [provider.id, data?.models ?? []] as const;
		}));
		setModels(Object.fromEntries(details));
		setModelProviderId((current) => current || body.providers?.[0]?.id || "");
	}, []);
	useEffect(() => { void load().catch((error: Error) => setMessage(error.message)); }, [load]);
	const create = async () => {
		setMessage("");
		const body = { displayName, baseUrl, apiProtocol: "anthropic-compatible" as const, enabled: true, apiKey };
		const { error } = editingProviderId
			? await apiClient.PATCH("/api/v1/providers/{providerId}", { params: { path: { providerId: editingProviderId } }, body })
			: await apiClient.POST("/api/v1/providers", { body });
		if (error) { setMessage("Provider could not be saved"); return; }
		setDisplayName(""); setBaseUrl(""); setApiKey(""); setEditingProviderId(""); setMessage("Provider saved"); await load();
	};
	const createModel = async () => {
		const body = { displayName: modelDisplayName, modelName, enabled: true, sortOrder: 0 };
		const { error } = editingModelId
			? await apiClient.PATCH("/api/v1/providers/{providerId}/models/{modelId}", { params: { path: { providerId: modelProviderId, modelId: editingModelId } }, body })
			: await apiClient.POST("/api/v1/providers/{providerId}/models", { params: { path: { providerId: modelProviderId } }, body });
		if (error) { setMessage("Provider model could not be saved"); return; }
		setModelDisplayName(""); setModelName(""); setEditingModelId(""); setMessage("Provider model saved"); await load();
	};
	const toggleProvider = async (provider: Provider) => {
		const { error } = await apiClient.PATCH("/api/v1/providers/{providerId}", { params: { path: { providerId: provider.id } }, body: { displayName: provider.displayName, baseUrl: provider.baseUrl, apiProtocol: provider.apiProtocol as "anthropic-compatible" | "openai-compatible", enabled: !provider.enabled } });
		if (error) { setMessage("Provider status could not be changed"); return; } await load();
	};
	const toggleModel = async (model: ProviderModel) => {
		const { error } = await apiClient.PATCH("/api/v1/providers/{providerId}/models/{modelId}", { params: { path: { providerId: model.providerId, modelId: model.id } }, body: { displayName: model.displayName, modelName: model.modelName, enabled: !model.enabled, sortOrder: model.sortOrder } });
		if (error) { setMessage("Model status could not be changed"); return; } await load();
	};
	const testConnection = async (providerId: string, providerModelId: string) => {
		const { data, error } = await apiClient.POST("/api/v1/providers/{providerId}/test", { params: { path: { providerId } }, body: { providerModelId } });
		setMessage(error ? "Connection test failed" : `${data?.ok ? "SUCCESS" : "FAILED"}: ${data?.message ?? "Unknown result"}`);
	};
	return <SettingsSection title="Providers" titleHidden={titleHidden} grouped>
		<div className="flex flex-col gap-3 p-3">
			<p className="text-xs text-muted-foreground">API keys are encrypted with Windows DPAPI and are never returned to this page.</p>
			{providers.map((provider) => <div className="rounded-md border border-border p-3" key={provider.id}>
				<div className="text-sm font-medium">{provider.displayName}</div>
				<div className="text-xs text-muted-foreground">{provider.apiProtocol} · {provider.baseUrl} · {provider.enabled ? "enabled" : "disabled"} · {provider.secretConfigured ? "credential configured" : "credential missing"}</div>
				<div className="mt-2 flex gap-2"><Button size="sm" variant="outline" onClick={() => { setEditingProviderId(provider.id); setDisplayName(provider.displayName); setBaseUrl(provider.baseUrl); setApiKey(""); }}>Edit</Button><Button size="sm" variant="outline" onClick={() => void toggleProvider(provider)}>{provider.enabled ? "Disable" : "Enable"}</Button></div>
				{(models[provider.id] ?? []).map((model) => <div className="mt-2 border-t border-border pt-2" key={model.id}>
					<div className="text-xs">{model.displayName} · {model.modelName} · {model.enabled ? "enabled" : "disabled"}</div>
					<div className="mt-1 flex gap-2"><Button size="sm" variant="outline" onClick={() => { setModelProviderId(provider.id); setEditingModelId(model.id); setModelDisplayName(model.displayName); setModelName(model.modelName); }}>Edit model</Button><Button size="sm" variant="outline" onClick={() => void toggleModel(model)}>{model.enabled ? "Disable model" : "Enable model"}</Button><Button disabled={!provider.enabled || !model.enabled} size="sm" variant="outline" onClick={() => void testConnection(provider.id, model.id)}>Test connection</Button></div>
				</div>)}
			</div>)}
			<div className="grid gap-2">
				<Input aria-label="Provider name" placeholder="Provider name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
				<Input aria-label="Base URL" placeholder="https://api.example.com/anthropic" value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} />
				<Input aria-label="API key" autoComplete="off" placeholder="API key (not displayed after save)" type="password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} />
				<Button disabled={!displayName.trim() || !baseUrl.trim()} onClick={() => void create()}>{editingProviderId ? "Save provider" : "Add provider"}</Button>
			</div>
			<div className="grid gap-2 border-t border-border pt-3">
				<select className="h-control-form rounded-md bg-input/50 px-3 text-sm" aria-label="Provider for model" value={modelProviderId} onChange={(event) => setModelProviderId(event.target.value)}>
					{providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.displayName}</option>)}
				</select>
				<Input aria-label="Model display name" placeholder="Model display name" value={modelDisplayName} onChange={(event) => setModelDisplayName(event.target.value)} />
				<Input aria-label="Provider model name" placeholder="Provider model identifier" value={modelName} onChange={(event) => setModelName(event.target.value)} />
				<Button disabled={!modelProviderId || !modelDisplayName.trim() || !modelName.trim()} onClick={() => void createModel()}>{editingModelId ? "Save model" : "Add model"}</Button>
			</div>
			{message && <p className="text-xs text-muted-foreground" role="status">{message}</p>}
		</div>
	</SettingsSection>;
}
