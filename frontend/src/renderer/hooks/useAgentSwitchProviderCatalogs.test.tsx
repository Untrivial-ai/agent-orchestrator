import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { act, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatConfigOption, ChatModel, ChatSkill } from "../types/conversation";
import {
	conversationConfigOptionsQueryKey,
	conversationModelsQueryKey,
	conversationSkillsQueryKey,
	useConversationConfigOptions,
	useConversationModels,
	useConversationSkills,
} from "./useConversation";

const { getMock, patchMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	patchMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, PATCH: patchMock },
	apiErrorMessage: () => "failed",
}));

import { useAgentSwitchProviderCatalogs } from "./useAgentSwitchProviderCatalogs";

const CLAUDE_OPTIONS = [
	{
		id: "model",
		name: "Model",
		category: "model",
		type: "select",
		currentValue: "opus-1m",
		choices: [{ value: "opus-1m", name: "Opus (1M context)" }],
	},
	{
		id: "effort",
		name: "Effort",
		category: "thought_level",
		type: "select",
		currentValue: "high",
		choices: [{ value: "high", name: "High" }],
	},
] satisfies ChatConfigOption[];

const CODEX_OPTIONS = [
	{
		id: "model",
		name: "Model",
		category: "model",
		type: "select",
		currentValue: "gpt-5.6-terra",
		choices: [{ value: "gpt-5.6-terra", name: "GPT-5.6 Terra" }],
	},
] satisfies ChatConfigOption[];

type ProviderName = "claude-code" | "codex";
type ProviderCatalog = {
	models: readonly ChatModel[];
	options: readonly ChatConfigOption[];
	skills: readonly ChatSkill[];
};

const PROVIDER_CATALOGS = {
	"claude-code": {
		models: [{ id: "opus-1m", displayName: "Opus", default: true }],
		options: CLAUDE_OPTIONS,
		skills: [{ name: "claude-commit", displayName: "Claude Commit" }],
	},
	codex: {
		models: [{ id: "gpt-5.6-terra", displayName: "GPT-5.6 Terra", default: true }],
		options: CODEX_OPTIONS,
		skills: [{ name: "codex-commit", displayName: "Codex Commit" }],
	},
} as const satisfies Record<ProviderName, ProviderCatalog>;

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

function catalogHarness(agentSwitching: boolean, observedSettledSwitchId?: string) {
	const catalogsEnabled = useAgentSwitchProviderCatalogs({
		sessionId: "sess-1",
		agentSwitching,
		settledSwitchId: observedSettledSwitchId,
	});
	const config = useConversationConfigOptions("sess-1", catalogsEnabled);
	const models = useConversationModels("sess-1", catalogsEnabled);
	const skills = useConversationSkills("sess-1", catalogsEnabled);
	return {
		catalogsEnabled,
		models: models.models,
		options: config.options,
		setOption: config.setOption,
		skills: skills.skills,
	};
}

function serveCatalog(getCatalog: () => ProviderCatalog) {
	getMock.mockImplementation(async (path: string) => {
		const catalog = getCatalog();
		if (path.endsWith("/models")) {
			return { data: { models: catalog.models }, error: undefined };
		}
		if (path.endsWith("/skills")) {
			return { data: { skills: catalog.skills }, error: undefined };
		}
		if (path.endsWith("/config-options")) {
			return { data: { options: catalog.options }, error: undefined };
		}
		throw new Error(`Unexpected GET ${path}`);
	});
}

beforeEach(() => {
	getMock.mockReset();
	patchMock.mockReset();
});

describe("useAgentSwitchProviderCatalogs", () => {
	it.each([
		["claude-code", "codex"],
		["codex", "claude-code"],
	] as const)(
		"refetches active catalog observers after a Chat switch from %s to %s",
		async (sourceProvider, targetProvider) => {
			const queryClient = new QueryClient({
				defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
			});
			let servedProvider: ProviderName = sourceProvider;
			serveCatalog(() => PROVIDER_CATALOGS[servedProvider]);
			const switchId = `switch-${sourceProvider}-${targetProvider}`;
			const { rerender, result } = renderHook(
				({ switching, settledSwitchId }: { switching: boolean; settledSwitchId?: string }) =>
					catalogHarness(switching, settledSwitchId),
				{
					initialProps: {
						switching: false,
						settledSwitchId: undefined as string | undefined,
					},
					wrapper: wrapper(queryClient),
				},
			);

			await waitFor(() => {
				expect(result.current.models[0]?.id).toBe(PROVIDER_CATALOGS[sourceProvider].models[0].id);
				expect(result.current.options[0]?.currentValue).toBe(
					PROVIDER_CATALOGS[sourceProvider].options[0].currentValue,
				);
				expect(result.current.skills[0]?.name).toBe(
					PROVIDER_CATALOGS[sourceProvider].skills[0].name,
				);
			});

			const staleSourceOptions = PROVIDER_CATALOGS[sourceProvider].options.map((option) =>
				option.id === "model" ? { ...option, currentValue: "delayed-model" } : option,
			);
			let resolveDelayedPatch:
				| ((value: {
						data: { options: readonly ChatConfigOption[] };
						error: undefined;
				  }) => void)
				| undefined;
			patchMock.mockReturnValue(
				new Promise((resolve) => {
					resolveDelayedPatch = resolve;
				}),
			);
			let delayedMutation!: Promise<unknown>;
			act(() => {
				delayedMutation = result.current.setOption("model", { value: "delayed-model" });
			});
			await waitFor(() => expect(patchMock).toHaveBeenCalledOnce());

			rerender({ switching: true, settledSwitchId: undefined });
			expect(result.current.catalogsEnabled).toBe(false);
			await waitFor(() => {
				expect(result.current.models).toEqual([]);
				expect(result.current.options).toEqual([]);
				expect(result.current.skills).toEqual([]);
			});

			servedProvider = targetProvider;
			// Durable success alone is not enough: catalogs stay paused until the target
			// controller is ready and the canonical lifecycle reports settlement.
			rerender({ switching: true, settledSwitchId: undefined });
			expect(result.current.catalogsEnabled).toBe(false);

			rerender({ switching: false, settledSwitchId: switchId });
			await waitFor(() => {
				expect(result.current.catalogsEnabled).toBe(true);
				expect(result.current.models[0]?.id).toBe(PROVIDER_CATALOGS[targetProvider].models[0].id);
				expect(result.current.options[0]?.currentValue).toBe(
					PROVIDER_CATALOGS[targetProvider].options[0].currentValue,
				);
				expect(result.current.skills[0]?.name).toBe(
					PROVIDER_CATALOGS[targetProvider].skills[0].name,
				);
			});

			resolveDelayedPatch!({ data: { options: staleSourceOptions }, error: undefined });
			await act(async () => delayedMutation);
			expect(result.current.options[0]?.currentValue).toBe(
				PROVIDER_CATALOGS[targetProvider].options[0].currentValue,
			);
		},
	);

	it("refetches recovered source catalogs after a failed Chat switch", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		let recovered = false;
		const recoveredCatalog = {
			...PROVIDER_CATALOGS["claude-code"],
			options: [{ ...CLAUDE_OPTIONS[0], currentValue: "opus-recovered" }],
		} as const;
		serveCatalog(() => (recovered ? recoveredCatalog : PROVIDER_CATALOGS["claude-code"]));
		const { rerender, result } = renderHook(
			({ switching, settledSwitchId }: { switching: boolean; settledSwitchId?: string }) =>
				catalogHarness(switching, settledSwitchId),
			{
				initialProps: {
					switching: true,
					settledSwitchId: undefined as string | undefined,
				},
				wrapper: wrapper(queryClient),
			},
		);
		expect(result.current.catalogsEnabled).toBe(false);

		recovered = true;
		rerender({ switching: false, settledSwitchId: "switch-failed-recovery" });

		await waitFor(() => {
			expect(result.current.catalogsEnabled).toBe(true);
			expect(result.current.options[0]?.currentValue).toBe("opus-recovered");
		});
	});

	it("reconciles stale catalogs when the first observed switch state is already completed", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		queryClient.setQueryData(conversationConfigOptionsQueryKey("sess-1"), CLAUDE_OPTIONS);
		serveCatalog(() => PROVIDER_CATALOGS.codex);

		const { result } = renderHook(
			() => catalogHarness(false, "switch-terminal-first"),
			{ wrapper: wrapper(queryClient) },
		);

		// Consumers stay gated until the terminal controller epoch has removed the
		// outgoing cache and fetched the live Codex catalog. renderHook flushes the
		// reconciliation effect, so the stale Claude value must already be gone.
		expect(result.current.options[0]?.currentValue).not.toBe(CLAUDE_OPTIONS[0].currentValue);
		await waitFor(() => {
			expect(result.current.catalogsEnabled).toBe(true);
			expect(result.current.options[0]?.currentValue).toBe(CODEX_OPTIONS[0].currentValue);
		});
	});

	it("clears stale catalogs on admission and refetches only after controller readiness", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		const removeQueries = vi.spyOn(queryClient, "removeQueries");
		const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
		serveCatalog(() => PROVIDER_CATALOGS["codex"]);

		const { rerender, result } = renderHook(
			({ switching, settledSwitchId }: { switching: boolean; settledSwitchId?: string }) =>
				catalogHarness(switching, settledSwitchId),
			{
				initialProps: {
					switching: true,
					settledSwitchId: undefined as string | undefined,
				},
				wrapper: wrapper(queryClient),
			},
		);

		await waitFor(() => {
			expect(removeQueries).toHaveBeenCalledWith({
				queryKey: conversationConfigOptionsQueryKey("sess-1"),
			});
		});
		expect(invalidateQueries).not.toHaveBeenCalledWith({
			queryKey: conversationConfigOptionsQueryKey("sess-1"),
		});

		rerender({ switching: true, settledSwitchId: undefined });
		expect(result.current.catalogsEnabled).toBe(false);

		rerender({ switching: true, settledSwitchId: undefined });
		expect(result.current.catalogsEnabled).toBe(false);

		rerender({ switching: false, settledSwitchId: "switch-1" });
		await waitFor(() => {
			expect(result.current.catalogsEnabled).toBe(true);
			expect(invalidateQueries).toHaveBeenCalledWith({
				queryKey: conversationConfigOptionsQueryKey("sess-1"),
			});
			expect(invalidateQueries).toHaveBeenCalledWith({
				queryKey: conversationModelsQueryKey("sess-1"),
			});
			expect(invalidateQueries).toHaveBeenCalledWith({
				queryKey: conversationSkillsQueryKey("sess-1"),
			});
			expect(result.current.options[0]?.currentValue).toBe(CODEX_OPTIONS[0].currentValue);
		});
	});

	it("keeps catalogs cleared when durable history requires recovery", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
		});
		queryClient.setQueryData(conversationConfigOptionsQueryKey("sess-1"), CLAUDE_OPTIONS);

		const { result } = renderHook(
			() =>
				useAgentSwitchProviderCatalogs({
					sessionId: "sess-1",
					agentSwitching: true,
				}),
			{ wrapper: wrapper(queryClient) },
		);

		expect(result.current).toBe(false);
		await waitFor(() => {
			expect(queryClient.getQueryData(conversationConfigOptionsQueryKey("sess-1"))).toBeUndefined();
		});
	});
});
