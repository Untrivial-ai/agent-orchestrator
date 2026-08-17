import { ChevronDown, RefreshCw, Search } from "lucide-react";
import { type ReactNode, useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AgentModelCatalog } from "../../hooks/useAgentModelsQuery";
import { useSuppressStrayFocusRing } from "../../hooks/useSuppressStrayFocusRing";
import { cn } from "../../lib/utils";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "../ui/dropdown-menu";

const MAX_VISIBLE_MODELS = 50;
const MODEL_SEARCH_THRESHOLD = 8;
const MAX_RECENT_MODELS = 3;
const RECENT_MODELS_STORAGE_KEY = "ao.recentModels.v1";

type AgentModel = NonNullable<AgentModelCatalog["models"]>[number];

type IndexedModel = {
	model: AgentModel;
	id: string;
	label: string;
	provider: string;
	normalizedID: string;
	normalizedLabel: string;
	normalizedProvider: string;
	index: number;
};

type ModelSearchIndex = {
	models: IndexedModel[];
	byID: Map<string, IndexedModel>;
	providerBuckets: Map<string, { models: IndexedModel[]; indexes: Set<number> }>;
	trigramPostings: Map<string, Set<number>>;
};

export type ModelSearchResult = {
	models: IndexedModel[];
	candidateCount: number;
	strategy: "direct" | "provider-index" | "text-index" | "fuzzy-fallback";
};

export function AgentModelCombobox({
	value,
	models,
	allowCustom,
	onChange,
	onCustom,
	onRefresh,
	refreshing = false,
	emptyLabel,
	triggerLabel,
	triggerClassName,
	menuAlign = "end",
	renderTrigger,
	recentScope,
	compact = false,
	disabled = false,
	"aria-label": ariaLabel,
}: {
	value: string;
	models: AgentModel[];
	allowCustom: boolean;
	onChange: (value: string) => void;
	onCustom: (value: string) => void;
	/** Rediscovery action, offered inside the menu instead of as standing chrome. */
	onRefresh?: () => void;
	refreshing?: boolean;
	/** Names what happens with no override, e.g. "Use codex's default". */
	emptyLabel?: string;
	triggerLabel?: string;
	triggerClassName?: string;
	menuAlign?: "start" | "center" | "end";
	renderTrigger?: (label: string) => ReactNode;
	/** Persists explicit model choices for this agent and pins them below defaults. */
	recentScope?: string;
	/** Flat "no override" + plain model names — no groups, badges, or refresh
	 *  action. Search still shows once the catalog passes MODEL_SEARCH_THRESHOLD,
	 *  same as non-compact mode; only the grouping/decoration is stripped. For
	 *  contexts where the menu should read like a simple choice, not a
	 *  model-management surface. */
	compact?: boolean;
	disabled?: boolean;
	"aria-label": string;
}) {
	const { t } = useTranslation();
	const [search, setSearch] = useState("");
	const [menuOpen, setMenuOpen] = useState(false);
	const [sessionRecentModels, setSessionRecentModels] = useState<Record<string, string[]>>({});
	const recentKey = recentScope ?? "";
	const storedRecentModels = useMemo(() => readRecentModels(recentScope), [recentScope]);
	const recentModelIDs = recentScope ? (sessionRecentModels[recentKey] ?? storedRecentModels) : [];
	const normalizedSearch = normalizeSearch(search);
	const searchIndex = useMemo(() => buildModelSearchIndex(models), [models]);
	const selected = searchIndex.byID.get(normalizeSearch(value));
	const showSearch = models.length > MODEL_SEARCH_THRESHOLD;

	const rankedModels = useMemo(() => {
		if (!normalizedSearch) {
			// Compact mode reads as a plain, stable list — picking a model
			// shouldn't reorder it to the top on the next open.
			return compact ? searchIndex.models : rankInitialModels(searchIndex.models, value, recentModelIDs);
		}
		return searchModelIndex(searchIndex, normalizedSearch).models;
	}, [compact, normalizedSearch, recentModelIDs, searchIndex, value]);

	const visibleModels = rankedModels.slice(0, MAX_VISIBLE_MODELS);
	const groups = useMemo(
		() =>
			compact
				? [{ key: "all", label: "", kind: "provider" as const, models: visibleModels }]
				: groupModels(visibleModels, normalizedSearch === "", value, recentModelIDs, {
						pinned: t("settings.models.currentDefaults"),
						recent: t("settings.models.recent"),
					}),
		[compact, normalizedSearch, recentModelIDs, t, value, visibleModels],
	);
	const customSearchValue = search.trim();
	const showCustomSearchAction = allowCustom && customSearchValue !== "" && rankedModels.length === 0;
	const noOverrideLabel = emptyLabel ?? t("settings.models.agentDefault");
	const currentLabel = triggerLabel ?? selected?.label ?? noOverrideLabel;
	const scrollRef = useRef<HTMLDivElement>(null);
	const [canScrollDown, setCanScrollDown] = useState(false);
	const updateScrollCue = useCallback(() => {
		const element = scrollRef.current;
		setCanScrollDown(Boolean(element && element.scrollHeight - element.scrollTop > element.clientHeight + 1));
	}, []);
	useLayoutEffect(() => {
		if (!menuOpen) {
			setCanScrollDown(false);
			return;
		}
		updateScrollCue();
		const element = scrollRef.current;
		if (!element || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateScrollCue);
		observer.observe(element);
		return () => observer.disconnect();
	}, [groups.length, menuOpen, normalizedSearch, showCustomSearchAction, updateScrollCue, visibleModels.length]);
	const onCloseAutoFocus = useSuppressStrayFocusRing(menuOpen);
	const selectModel = (modelID: string) => {
		if (recentScope) {
			const next = rememberRecentModel(recentScope, modelID);
			setSessionRecentModels((current) => ({ ...current, [recentScope]: next }));
		}
		onChange(modelID);
	};

	return (
		<DropdownMenu
			onOpenChange={(open) => {
				setMenuOpen(open);
				if (!open) setSearch("");
			}}
		>
			<DropdownMenuTrigger asChild disabled={disabled}>
				<button
					type="button"
					className={cn(
						"group/agent-model-trigger settings-option-trigger max-w-full min-w-0 hover:text-settings-label focus:outline-none focus-visible:outline-none focus-visible:ring-0 data-[state=open]:outline-none data-[state=open]:ring-0",
						disabled && "cursor-not-allowed opacity-50",
						triggerClassName,
					)}
					aria-label={ariaLabel}
					disabled={disabled}
				>
					{renderTrigger ? (
						renderTrigger(currentLabel)
					) : (
						<span className="min-w-0 truncate">{currentLabel}</span>
					)}
					<ChevronDown
						className="size-icon-sm shrink-0 opacity-70 transition-transform duration-300 ease-out group-data-[state=open]/agent-model-trigger:rotate-180"
						aria-hidden="true"
					/>
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align={menuAlign}
				onCloseAutoFocus={onCloseAutoFocus}
				className="settings-menu-surface max-h-select-menu-max! w-[min(22rem,calc(100vw-2rem))] overflow-hidden! rounded-(--radius-settings-panel) border-settings-menu bg-settings-menu"
			>
				{showSearch && (
					<div className="relative shrink-0 p-1" onKeyDown={(event) => event.stopPropagation()}>
						<Search
							className="pointer-events-none absolute left-3.5 top-1/2 size-icon-sm -translate-y-1/2 text-settings-muted"
							aria-hidden="true"
						/>
						<input
							type="search"
							aria-label={t("settings.models.searchAria", { label: ariaLabel.toLocaleLowerCase() })}
							value={search}
							onChange={(event) => setSearch(event.target.value)}
							placeholder={t("settings.models.searchPlaceholder")}
							className="menu-search-input pl-8!"
						/>
					</div>
				)}

				<div className="relative grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)] overflow-hidden">
					<div
						ref={scrollRef}
						className="model-menu-scroll min-h-0 overflow-y-auto overscroll-contain"
						onScroll={updateScrollCue}
					>
						{normalizedSearch === "" && (
							<DropdownMenuItem onSelect={() => onChange("")} className={modelItemClass(value === "")}>
								{noOverrideLabel}
							</DropdownMenuItem>
						)}

						{groups.map((group, groupIndex) => (
							<div key={group.key}>
								{!compact && (groupIndex > 0 || normalizedSearch === "") && <DropdownMenuSeparator />}
								{!compact && <DropdownMenuLabel className="normal-case tracking-normal">{group.label}</DropdownMenuLabel>}
								{group.models.map((item) =>
									compact ? (
										<DropdownMenuItem
											key={item.id}
											onSelect={() => selectModel(item.id)}
											className={modelItemClass(item.id === value)}
										>
											<span className="truncate text-settings-label">{item.label}</span>
										</DropdownMenuItem>
									) : (
										<DropdownMenuItem
											key={item.id}
											onSelect={() => selectModel(item.id)}
											className={modelItemClass(item.id === value)}
										>
											<div className="flex min-w-0 flex-1 items-center gap-3">
												<div className="min-w-0 flex-1">
													<div className="flex items-center gap-2">
														<span className="truncate text-settings-label">{item.label}</span>
														{item.model.isDefault && (
															<span className="rounded-full bg-settings-menu-selected px-1.5 py-0.5 text-micro text-settings-muted">
																{t("settings.models.default")}
															</span>
														)}
													</div>
													{shouldShowModelID(item, visibleModels, normalizedSearch) && (
														<p className="truncate text-xs text-settings-muted">{item.id}</p>
													)}
												</div>
												{group.kind !== "provider" && item.provider !== "Other" && (
													<span className="shrink-0 text-xs text-settings-muted">{item.provider}</span>
												)}
											</div>
										</DropdownMenuItem>
									),
								)}
							</div>
						))}

						{showCustomSearchAction && (
							<DropdownMenuItem onSelect={() => onCustom(customSearchValue)} className={modelItemClass(false)}>
								{t("settings.models.useCustom", { model: customSearchValue })}
							</DropdownMenuItem>
						)}
						{normalizedSearch !== "" && rankedModels.length === 0 && !allowCustom && (
							<p className="px-2 py-1.5 text-xs text-settings-muted">{t("settings.models.noMatches")}</p>
						)}
						{normalizedSearch === "" && allowCustom && (
							<>
								<DropdownMenuSeparator />
								<DropdownMenuItem onSelect={() => onCustom("")} className={modelItemClass(false)}>
									{t("settings.models.custom")}
								</DropdownMenuItem>
							</>
						)}
						{/* Rediscovery lives here, not as a standing link beside the field: it is
						    a rare repair action, and the daemon revalidates a stale catalog on its
						    own. Keeping it in the menu costs no layout in the calm state. */}
						{!compact && onRefresh && (
							<>
								<DropdownMenuSeparator />
								<DropdownMenuItem
									disabled={refreshing}
									onSelect={(event) => {
										event.preventDefault();
										onRefresh();
									}}
									className={modelItemClass(false)}
								>
									<RefreshCw
										className={cn("size-icon-sm shrink-0 opacity-70", refreshing && "animate-spin")}
										aria-hidden="true"
									/>
									{refreshing ? t("settings.models.refreshing") : t("settings.models.refreshList")}
								</DropdownMenuItem>
							</>
						)}
						{showSearch && (
							<p className="px-2 py-1.5 text-xs text-settings-muted" aria-live="polite">
								{t("settings.models.matchingCount", {
									visible: visibleModels.length.toLocaleString(),
									total: rankedModels.length.toLocaleString(),
								})}
								{normalizedSearch === "" && rankedModels.length > MAX_VISIBLE_MODELS
									? t("settings.models.typeToNarrow")
									: ""}
							</p>
						)}
					</div>
					<div
						className={cn("model-menu-overflow-cue", canScrollDown ? "opacity-100" : "opacity-0")}
						aria-hidden="true"
					/>
				</div>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function normalizeSearch(value: string): string {
	return value.trim().toLocaleLowerCase();
}

function recentModelsStorage(): Storage | undefined {
	if (typeof window === "undefined") return undefined;
	try {
		return window.localStorage;
	} catch {
		return undefined;
	}
}

function readRecentModelMap(): Record<string, string[]> {
	const storage = recentModelsStorage();
	if (!storage) return {};
	try {
		const parsed: unknown = JSON.parse(storage.getItem(RECENT_MODELS_STORAGE_KEY) ?? "{}");
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
		const result: Record<string, string[]> = {};
		for (const [scope, values] of Object.entries(parsed)) {
			if (!Array.isArray(values)) continue;
			result[scope] = values
				.filter((value): value is string => typeof value === "string")
				.slice(0, MAX_RECENT_MODELS);
		}
		return result;
	} catch {
		return {};
	}
}

function readRecentModels(scope: string | undefined): string[] {
	return scope ? (readRecentModelMap()[scope] ?? []) : [];
}

function rememberRecentModel(scope: string, modelID: string): string[] {
	const recentModels = readRecentModelMap();
	const next = [modelID, ...(recentModels[scope] ?? []).filter((id) => id !== modelID)].slice(
		0,
		MAX_RECENT_MODELS,
	);
	recentModels[scope] = next;
	try {
		recentModelsStorage()?.setItem(RECENT_MODELS_STORAGE_KEY, JSON.stringify(recentModels));
	} catch {
		// Recents are optional polish; model selection must still work without storage.
	}
	return next;
}

function shouldShowModelID(item: IndexedModel, siblings: IndexedModel[], search: string): boolean {
	if (item.id === item.label) return false;

	const label = normalizeSearch(item.label);
	const duplicateLabel = siblings.some(
		(candidate) => candidate.id !== item.id && normalizeSearch(candidate.label) === label,
	);
	if (duplicateLabel) return true;

	return search !== "" && normalizeSearch(item.id).includes(search) && !label.includes(search);
}

function providerFromModelID(modelID: string): string {
	const slash = modelID.indexOf("/");
	return slash > 0 ? modelID.slice(0, slash) : "";
}

export function buildModelSearchIndex(models: AgentModel[]): ModelSearchIndex {
	const indexedModels = models.map((model, index) => {
		const label = model.label || model.id;
		const provider = model.provider?.trim() || providerFromModelID(model.id) || "Other";
		return {
			model,
			id: model.id,
			label,
			provider,
			normalizedID: normalizeSearch(model.id),
			normalizedLabel: normalizeSearch(label),
			normalizedProvider: normalizeSearch(provider),
			index,
		};
	});
	const byID = new Map(indexedModels.map((item) => [item.normalizedID, item]));
	const providerBuckets = new Map<string, { models: IndexedModel[]; indexes: Set<number> }>();
	const trigramPostings = new Map<string, Set<number>>();

	for (const item of indexedModels) {
		const providerKeys = new Set([
			item.normalizedProvider,
			normalizeSearch(providerFromModelID(item.id)),
		]);
		for (const providerKey of providerKeys) {
			if (!providerKey) continue;
			const bucket = providerBuckets.get(providerKey) ?? { models: [], indexes: new Set<number>() };
			bucket.models.push(item);
			bucket.indexes.add(item.index);
			providerBuckets.set(providerKey, bucket);
		}

		const itemTrigrams = new Set([
			...trigrams(item.normalizedID),
			...trigrams(item.normalizedLabel),
			...trigrams(item.normalizedProvider),
		]);
		for (const trigram of itemTrigrams) {
			const posting = trigramPostings.get(trigram) ?? new Set<number>();
			posting.add(item.index);
			trigramPostings.set(trigram, posting);
		}
	}

	return { models: indexedModels, byID, providerBuckets, trigramPostings };
}

export function searchModelIndex(index: ModelSearchIndex, query: string): ModelSearchResult {
	const normalizedQuery = normalizeSearch(query);
	const directMatch = index.byID.get(normalizedQuery);
	if (directMatch) {
		return { models: [directMatch], candidateCount: 1, strategy: "direct" };
	}

	const provider = providerQualifier(normalizedQuery);
	const providerBucket = provider ? index.providerBuckets.get(provider) : undefined;
	const universe = providerBucket?.models ?? index.models;
	const indexedCandidates = trigramCandidates(index, normalizedQuery, providerBucket?.indexes);
	if (indexedCandidates.length > 0) {
		return {
			models: rankMatches(indexedCandidates, normalizedQuery),
			candidateCount: indexedCandidates.length,
			strategy: providerBucket ? "provider-index" : "text-index",
		};
	}

	return {
		models: rankMatches(universe, normalizedQuery),
		candidateCount: universe.length,
		strategy: "fuzzy-fallback",
	};
}

function rankInitialModels(models: IndexedModel[], selectedID: string, recentIDs: string[]): IndexedModel[] {
	const byID = new Map(models.map((item) => [normalizeSearch(item.id), item]));
	const result: IndexedModel[] = [];
	const added = new Set<string>();
	const append = (item: IndexedModel | undefined) => {
		if (!item || added.has(item.id)) return;
		added.add(item.id);
		result.push(item);
	};

	append(byID.get(normalizeSearch(selectedID)));
	for (const item of models) {
		if (item.model.isDefault) append(item);
	}
	for (const recentID of recentIDs) {
		append(byID.get(normalizeSearch(recentID)));
	}
	for (const item of models) append(item);
	return result;
}

function providerQualifier(query: string): string {
	const slash = query.indexOf("/");
	return slash > 0 ? query.slice(0, slash) : "";
}

function trigrams(value: string): string[] {
	if (value.length < 3) return [];
	const result: string[] = [];
	for (let index = 0; index <= value.length - 3; index += 1) {
		result.push(value.slice(index, index + 3));
	}
	return result;
}

function trigramCandidates(
	index: ModelSearchIndex,
	query: string,
	providerIndexes: Set<number> | undefined,
): IndexedModel[] {
	const queryTrigrams = [...new Set(trigrams(query))];
	if (queryTrigrams.length === 0) return [];
	const postings = queryTrigrams.map((trigram) => index.trigramPostings.get(trigram));
	if (postings.some((posting) => !posting)) return [];
	const completePostings = postings as Set<number>[];
	const smallestPosting = completePostings.reduce((smallest, posting) =>
		posting.size < smallest.size ? posting : smallest,
	);
	const matches: IndexedModel[] = [];
	for (const modelIndex of smallestPosting) {
		if (providerIndexes && !providerIndexes.has(modelIndex)) continue;
		if (completePostings.every((posting) => posting.has(modelIndex))) {
			matches.push(index.models[modelIndex]);
		}
	}
	return matches;
}

function rankMatches(models: IndexedModel[], query: string): IndexedModel[] {
	return models
		.map((item) => ({ item, score: modelMatchScore(item, query) }))
		.filter((match): match is { item: IndexedModel; score: number } => match.score !== null)
		.sort((a, b) => a.score - b.score || a.item.index - b.item.index)
		.map((match) => match.item);
}

function modelMatchScore(item: IndexedModel, query: string): number | null {
	const id = item.normalizedID;
	const label = item.normalizedLabel;
	const provider = item.normalizedProvider;
	if (id === query) return 0;
	if (id.startsWith(query)) return 10;
	if (label.startsWith(query)) return 20;
	if (provider.startsWith(query)) return 30;
	if (id.includes(query)) return 40;
	if (label.includes(query)) return 50;
	if (provider.includes(query)) return 60;
	const fuzzyScores = [id, label, provider]
		.map((candidate) => fuzzySubsequenceScore(candidate, query))
		.filter((score): score is number => score !== null);
	return fuzzyScores.length === 0 ? null : 100 + Math.min(...fuzzyScores);
}

function fuzzySubsequenceScore(haystack: string, needle: string): number | null {
	let searchAt = 0;
	let score = 0;
	for (const character of needle) {
		const foundAt = haystack.indexOf(character, searchAt);
		if (foundAt === -1) return null;
		score += foundAt - searchAt;
		searchAt = foundAt + 1;
	}
	return score;
}

type ModelGroup = {
	key: string;
	label: string;
	kind: "pinned" | "recent" | "provider";
	models: IndexedModel[];
};

function groupModels(
	models: IndexedModel[],
	showPinned: boolean,
	selectedID: string,
	recentIDs: string[],
	labels: { pinned: string; recent: string },
) {
	const groups = new Map<string, ModelGroup>();
	const recentSet = new Set(recentIDs);
	for (const item of models) {
		const pinned = showPinned && (item.id === selectedID || item.model.isDefault);
		const recent = showPinned && !pinned && recentSet.has(item.id);
		const kind: ModelGroup["kind"] = pinned ? "pinned" : recent ? "recent" : "provider";
		const key = kind === "provider" ? `provider:${item.provider}` : kind;
		const group = groups.get(key) ?? {
			key,
			label: kind === "pinned" ? labels.pinned : kind === "recent" ? labels.recent : item.provider,
			kind,
			models: [],
		};
		group.models.push(item);
		groups.set(key, group);
	}
	return [...groups.values()];
}

function modelItemClass(selected: boolean): string {
	return cn(
		"settings-menu-item min-w-0 cursor-default outline-none",
		"focus:bg-settings-menu-selected focus:text-settings-title",
		"data-highlighted:bg-settings-menu-selected data-highlighted:text-settings-title",
		selected && "border-settings-menu bg-settings-menu-selected text-settings-title",
	);
}
