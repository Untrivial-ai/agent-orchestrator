import type { SearchAddon } from "@xterm/addon-search";
import { CaseSensitive, ChevronDown, ChevronUp, Regex, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";

type SearchOptions = Parameters<SearchAddon["findNext"]>[1];

const decorations = {
	activeMatchBackground: "#c4580e",
	activeMatchBorder: "#ffcf6b",
	activeMatchColorOverviewRuler: "#ff9900",
	matchBackground: "#5c4a00",
	matchBorder: "#5c4a00",
	matchOverviewRuler: "#ffcc00",
} satisfies NonNullable<SearchOptions>["decorations"];

/**
 * xterm can briefly retain a match column beyond the live grid after reflow.
 * Its decoration registration then throws instead of merely skipping the
 * highlight. Search navigation has already happened at that point, so drop
 * only this known decoration failure rather than taking down the terminal.
 */
export function safeTerminalFind(
	find: (query: string, options?: SearchOptions) => boolean,
	query: string,
	options?: SearchOptions,
): boolean {
	try {
		return find(query, options);
	} catch (error) {
		if (error instanceof Error && /only accepts positive integers/i.test(error.message)) return false;
		throw error;
	}
}

type TerminalSearchProps = {
	onClose: () => void;
	onReturnFocus: () => void;
	open: boolean;
	searchAddon: SearchAddon | null;
};

type SearchResults = { resultCount: number; resultIndex: number };

function isValidRegex(query: string): boolean {
	try {
		new RegExp(query);
		return true;
	} catch {
		return false;
	}
}

export function TerminalSearch({ onClose, onReturnFocus, open, searchAddon }: TerminalSearchProps) {
	const { t } = useTranslation();
	const inputRef = useRef<HTMLInputElement | null>(null);
	const [caseSensitive, setCaseSensitive] = useState(false);
	const [query, setQuery] = useState("");
	const [regex, setRegex] = useState(false);
	const [results, setResults] = useState<SearchResults>({ resultCount: 0, resultIndex: -1 });
	const queryIsValid = !regex || isValidRegex(query);

	const clearSearch = useCallback(() => {
		if (!searchAddon) return;
		searchAddon.clearDecorations();
		// xterm 5 can leave the active result selected after decorations clear.
		safeTerminalFind((value, options) => searchAddon.findNext(value, options), "");
		setResults({ resultCount: 0, resultIndex: -1 });
	}, [searchAddon]);

	const searchOptions = useCallback(
		(incremental = false): SearchOptions => ({
			caseSensitive,
			decorations,
			incremental,
			regex,
		}),
		[caseSensitive, regex],
	);

	const find = useCallback(
		(direction: "next" | "previous") => {
			if (!searchAddon || !query || !queryIsValid) return;
			const run =
				direction === "next"
					? searchAddon.findNext.bind(searchAddon)
					: searchAddon.findPrevious.bind(searchAddon);
			safeTerminalFind(run, query, searchOptions());
			inputRef.current?.focus();
		},
		[query, queryIsValid, searchAddon, searchOptions],
	);

	const close = useCallback(() => {
		clearSearch();
		onClose();
		onReturnFocus();
	}, [clearSearch, onClose, onReturnFocus]);

	useEffect(() => {
		if (!searchAddon) return undefined;
		const subscription = searchAddon.onDidChangeResults(setResults);
		return () => subscription.dispose();
	}, [searchAddon]);

	useEffect(() => {
		if (!open) {
			clearSearch();
			return;
		}
		inputRef.current?.focus();
		inputRef.current?.select();
	}, [clearSearch, open]);

	useEffect(() => {
		if (!open || !searchAddon || !query || !queryIsValid) {
			if (open && (!query || !queryIsValid)) clearSearch();
			return;
		}
		safeTerminalFind(
			(value, options) => searchAddon.findNext(value, options),
			query,
			searchOptions(true),
		);
	}, [clearSearch, open, query, queryIsValid, searchAddon, searchOptions]);

	if (!open) return null;

	const currentResult = results.resultCount > 0 && results.resultIndex >= 0 ? results.resultIndex + 1 : 0;
	const resultLabel = queryIsValid ? `${currentResult}/${results.resultCount}` : "—";
	const keepInputFocus = (event: ReactMouseEvent) => event.preventDefault();

	return (
		<div
			aria-label={t("terminal.search")}
			className="absolute right-3 top-2 z-20 flex h-9 w-[22rem] max-w-[calc(100%-1.5rem)] items-center gap-0.5 rounded-md border border-border bg-surface/95 px-1 shadow-lg backdrop-blur-sm"
			data-testid="terminal-search"
			onKeyDown={(event) => {
				event.stopPropagation();
				if (event.key === "Escape") {
					event.preventDefault();
					close();
				} else if (event.key === "Enter") {
					event.preventDefault();
					find(event.shiftKey ? "previous" : "next");
				} else if (event.key.toLowerCase() === "f" && (event.metaKey || event.ctrlKey)) {
					event.preventDefault();
					inputRef.current?.select();
				} else if (event.key.toLowerCase() === "g" && (event.metaKey || event.ctrlKey)) {
					event.preventDefault();
					find(event.shiftKey ? "previous" : "next");
				}
			}}
			role="search"
		>
			<input
				aria-label={t("terminal.search")}
				aria-invalid={!queryIsValid || undefined}
				className="h-7 min-w-0 flex-1 bg-transparent px-2 font-mono text-xs text-foreground outline-none placeholder:text-terminal-dim aria-invalid:text-destructive"
				maxLength={512}
				onChange={(event) => setQuery(event.target.value)}
				placeholder={t("terminal.searchPlaceholder")}
				ref={inputRef}
				type="search"
				value={query}
			/>
			<span
				aria-label={
					queryIsValid
						? t("terminal.searchResults", { current: currentResult, total: results.resultCount })
						: t("terminal.searchInvalidRegex")
				}
				aria-live="polite"
				className="min-w-9 text-center font-mono text-2xs tabular-nums text-terminal-dim"
			>
				{resultLabel}
			</span>
			<Button
				aria-label={t("terminal.searchCaseSensitive")}
				aria-pressed={caseSensitive}
				className={cn("size-7 text-muted-foreground", caseSensitive && "bg-muted text-primary")}
				onClick={() => setCaseSensitive((current) => !current)}
				onMouseDown={keepInputFocus}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<CaseSensitive className="size-icon-sm" aria-hidden="true" />
			</Button>
			<Button
				aria-label={t("terminal.searchRegex")}
				aria-pressed={regex}
				className={cn("size-7 text-muted-foreground", regex && "bg-muted text-primary")}
				onClick={() => setRegex((current) => !current)}
				onMouseDown={keepInputFocus}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<Regex className="size-icon-sm" aria-hidden="true" />
			</Button>
			<div className="mx-0.5 h-4 w-px bg-border" />
			<Button
				aria-label={t("terminal.searchPrevious")}
				className="size-7 text-muted-foreground"
				disabled={!query || !queryIsValid}
				onClick={() => find("previous")}
				onMouseDown={keepInputFocus}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<ChevronUp className="size-icon-sm" aria-hidden="true" />
			</Button>
			<Button
				aria-label={t("terminal.searchNext")}
				className="size-7 text-muted-foreground"
				disabled={!query || !queryIsValid}
				onClick={() => find("next")}
				onMouseDown={keepInputFocus}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<ChevronDown className="size-icon-sm" aria-hidden="true" />
			</Button>
			<Button
				aria-label={t("files.closeSearch")}
				className="size-7 text-muted-foreground"
				onClick={close}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<X className="size-icon-sm" aria-hidden="true" />
			</Button>
		</div>
	);
}
