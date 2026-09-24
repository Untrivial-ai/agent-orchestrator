import { Search } from "lucide-react";
import { useCallback, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";

/** Mounted inside menu content so closing either menu also clears its query. */
export function ModelMenuChoices<T extends { id: string; label: string }>({
	models,
	children,
}: {
	models: T[];
	children: (models: T[]) => ReactNode;
}) {
	const { t } = useTranslation();
	const [search, setSearch] = useState("");
	const showSearch = models.length >= 10;
	const query = showSearch ? search.trim() : "";
	const normalizedQuery = query.toLocaleLowerCase();
	const matches = useMemo(() => {
		if (!normalizedQuery) return models;
		return models.filter((model) => model.label.toLocaleLowerCase().includes(normalizedQuery));
	}, [models, normalizedQuery]);
	const searchRef = useRef<HTMLInputElement>(null);
	const scrollRef = useRef<HTMLDivElement>(null);
	const [canScrollDown, setCanScrollDown] = useState(false);
	const updateScrollCue = useCallback(() => {
		const element = scrollRef.current;
		setCanScrollDown(Boolean(element && element.scrollHeight - element.scrollTop > element.clientHeight + 1));
	}, []);
	useLayoutEffect(() => {
		if (scrollRef.current) scrollRef.current.scrollTop = 0;
		updateScrollCue();
		const element = scrollRef.current;
		if (!element || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateScrollCue);
		observer.observe(element);
		return () => observer.disconnect();
	}, [matches, updateScrollCue]);

	return (
		<div className="flex min-h-0 max-h-[calc(var(--size-select-menu-max)-var(--space-2)*2)] flex-col">
			{showSearch && (
				<div
					// No inline padding: the field then shares the menu rows' silhouette,
					// since their highlight spans the same content box.
					className="relative shrink-0 pb-1"
					onClick={(event) => event.stopPropagation()}
					onKeyDown={(event) => {
						if (event.nativeEvent.isComposing) {
							event.stopPropagation();
							return;
						}
						if (event.key === "Escape") return;
						event.stopPropagation();
						if (event.key === "ArrowDown" || event.key === "ArrowUp") {
							event.preventDefault();
							const items = scrollRef.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]');
							const target = event.key === "ArrowDown" ? items?.[0] : items?.[items.length - 1];
							target?.focus();
						}
					}}
				>
					<div className="relative">
						<Search
							className="pointer-events-none absolute left-3 top-1/2 size-icon-sm -translate-y-1/2 text-settings-muted"
							aria-hidden="true"
						/>
						<input
							ref={searchRef}
							type="search"
							aria-label={t("settings.models.searchAria", { label: "models" })}
							value={search}
							onChange={(event) => setSearch(event.target.value)}
							placeholder={t("settings.models.searchPlaceholder")}
							className="menu-search-input h-control-form! rounded-[10px] pl-8!"
						/>
					</div>
				</div>
			)}
			<div className="relative grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)] overflow-hidden">
				<div
					ref={scrollRef}
					className="model-menu-scroll min-h-0 overflow-y-auto overscroll-contain"
					onScroll={updateScrollCue}
					onKeyDownCapture={(event) => {
						if (!searchRef.current) return;
						const firstItem = event.currentTarget.querySelector('[role="menuitemradio"]');
						if ((event.key === "ArrowUp" && event.target === firstItem) || (event.key === "Tab" && event.shiftKey)) {
							// Return to search before the menu's roving focus handles the key.
							event.preventDefault();
							event.stopPropagation();
							searchRef.current.focus();
							return;
						}
						if (event.key === "Backspace") {
							event.preventDefault();
							event.stopPropagation();
							searchRef.current.focus();
							setSearch((current) => current.slice(0, -1));
							return;
						}
						if (event.key.length === 1 && event.key !== " " && !event.ctrlKey && !event.metaKey && !event.altKey) {
							// Narrow the catalog instead of letting the menu's typeahead jump
							// to whichever row starts with the typed character. Space stays
							// with the menu, where it selects the focused row.
							event.preventDefault();
							event.stopPropagation();
							searchRef.current.focus();
							setSearch((current) => current + event.key);
						}
					}}
				>
					{children(matches)}
					{matches.length === 0 && (
						<p className="px-2 py-1.5 text-xs text-settings-muted">{t("settings.models.noMatches")}</p>
					)}
				</div>
				<div
					className={cn("model-menu-overflow-cue", canScrollDown ? "opacity-100" : "opacity-0")}
					aria-hidden="true"
				/>
			</div>
			{showSearch && (
				<p className="shrink-0 px-2 py-1.5 text-xs text-settings-muted" aria-live="polite">
					{t("settings.models.matchingCount", {
						visible: matches.length.toLocaleString(),
						total: matches.length.toLocaleString(),
					})}
				</p>
			)}
		</div>
	);
}
