/**
 * The composer's suggestion list.
 *
 * Presentation only: it renders ranked rows and reports clicks. Which rows exist,
 * which is highlighted, and what a selection inserts are all decided by the
 * composer, so the keyboard and the mouse cannot disagree about what is selected.
 *
 * Not a Popover or a Command: both want focus. The editor must keep it — the user
 * is still typing, and every keystroke has to reach the field and re-filter the
 * list. So this is a plain positioned panel wired to the editor's own key
 * handling, following the listbox pattern (`aria-activedescendant` on the input
 * rather than a focus move).
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowDownUp, CornerDownLeft } from "lucide-react";
import { cn } from "../../lib/utils";
import { composerFileIcon } from "./composerFileIcon";
import type { Suggestion, TriggerKind } from "./composerSuggest";

export function ComposerSuggestMenu({
	id,
	kind,
	items,
	highlighted,
	onPick,
	truncated,
}: {
	/** Shared with the editor's `aria-controls`, so the pairing is announced. */
	id: string;
	kind: TriggerKind;
	items: Suggestion[];
	highlighted: number;
	onPick: (value: string) => void;
	/** The candidate list was capped by the daemon, so it is not exhaustive. */
	truncated?: boolean;
}) {
	const list = useRef<HTMLUListElement>(null);
	const lastScrollTop = useRef(0);
	const scrollDirection = useRef<"up" | "down" | null>(null);
	const previousHighlighted = useRef(highlighted);
	const [scrollIndicators, setScrollIndicators] = useState({
		top: false,
		bottom: true,
	});

	const updateScrollIndicators = useCallback(() => {
		const node = list.current;
		if (!node) return;
		const delta = node.scrollTop - lastScrollTop.current;
		if (delta > 1) scrollDirection.current = "down";
		if (delta < -1) scrollDirection.current = "up";
		lastScrollTop.current = node.scrollTop;

		const top = node.scrollTop > 1;
		const bottom = node.scrollTop + node.clientHeight < node.scrollHeight - 1;
		const indicators =
			scrollDirection.current === "down"
				? { top, bottom: false }
				: scrollDirection.current === "up"
					? { top: false, bottom }
					: { top: false, bottom };
		setScrollIndicators((current) =>
			current.top === indicators.top && current.bottom === indicators.bottom ? current : indicators,
		);
	}, []);

	// Keep the highlighted row visible when it moves by keyboard past the edge of
	// the scroll area.
	useEffect(() => {
		const previous = previousHighlighted.current;
		previousHighlighted.current = highlighted;
		// Index zero is visible whenever the menu opens or its results re-rank.
		// Calling scrollIntoView for it forced a synchronous layout on every typed
		// character, which made `/` and `@` suggestions hitch the composer. It
		// still needs a scroll when filtering reset a keyboard selection to zero.
		if (highlighted === 0 && previous === 0 && (list.current?.scrollTop ?? 0) <= 1) return;
		const row = list.current?.querySelector<HTMLElement>(`[data-index="${highlighted}"]`);
		row?.scrollIntoView({ block: "nearest" });
	}, [highlighted, items]);

	useEffect(() => {
		const node = list.current;
		scrollDirection.current = null;
		lastScrollTop.current = node?.scrollTop ?? 0;
		updateScrollIndicators();
		if (!node || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateScrollIndicators);
		observer.observe(node);
		return () => observer.disconnect();
	}, [items, updateScrollIndicators]);

	if (items.length === 0) return null;

	return (
		<div
			className="absolute bottom-full left-0 z-overlay mb-1.5 flex w-full max-w-md flex-col gap-px overflow-hidden rounded-lg border border-border bg-card p-1 text-popover-foreground"
			// The composer owns the keyboard; a pointerdown here must not pull focus
			// out of the textarea on its way to the click.
			onMouseDown={(event) => event.preventDefault()}
		>
			<div className="flex items-start justify-between gap-2 px-2 py-1">
				<span className="text-micro tracking-wide text-muted-foreground">
					{kind === "skill" ? "Skills" : "Files in this worktree"}
				</span>
				<ArrowDownUp
					aria-label="Use the up and down arrow keys to navigate"
					className="size-3.5 text-muted-foreground"
				/>
			</div>

			<div className="relative min-h-0">
				<ul
					ref={list}
					id={id}
					role="listbox"
					onScroll={updateScrollIndicators}
					className="flex max-h-64 flex-col gap-px overflow-y-auto"
				>
					{items.map((item, index) => (
						<li key={item.value} data-index={index}>
							<button
								type="button"
								role="option"
								id={`${id}-option-${index}`}
								aria-selected={index === highlighted}
								onClick={() => onPick(item.value)}
								className={cn(
									"flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left text-control text-muted-foreground outline-none transition-none",
									index === highlighted
										? "bg-interactive-active text-foreground"
										: "bg-transparent hover:bg-interactive-hover hover:text-foreground",
								)}
							>
								{kind === "file"
									? (() => {
											const Icon = composerFileIcon(item.value);
											return (
												<span
													aria-hidden="true"
													className="flex h-4 shrink-0 items-center text-muted-foreground"
												>
													<Icon className="size-3.5" />
												</span>
											);
										})()
									: null}
								<span className="min-w-0 flex-1">
									<span
										className={cn(
											"block truncate text-xs",
											index === highlighted ? "text-foreground" : "text-muted-foreground",
										)}
									>
										{kind === "skill" ? `/${item.label}` : item.label}
									</span>
									{item.detail ? (
										<span className="block truncate text-[11px] leading-snug text-muted-foreground">
											{item.detail}
										</span>
									) : null}
								</span>
								{displayBadge(item.badge) ? (
									<span className="shrink-0 text-micro tracking-wide text-muted-foreground">
										{displayBadge(item.badge)}
									</span>
								) : null}
								{index === highlighted ? (
									<span
										aria-label="Press Tab or Enter to insert"
										className="flex shrink-0 items-center gap-1 text-micro text-muted-foreground"
									>
										<kbd className="rounded border border-border-strong bg-background/40 px-1 py-0.5 font-sans text-[10px] leading-none">
											Tab
										</kbd>
										<CornerDownLeft aria-hidden="true" className="size-3" />
									</span>
								) : null}
							</button>
						</li>
					))}
				</ul>
				{scrollIndicators.top ? (
					<div
						aria-hidden="true"
						className="pointer-events-none absolute inset-x-0 top-0 z-10 h-5 bg-gradient-to-b from-card to-transparent"
					/>
				) : null}
				{scrollIndicators.bottom ? (
					<div
						aria-hidden="true"
						className="pointer-events-none absolute inset-x-0 bottom-0 z-10 h-5 bg-gradient-to-t from-card to-transparent"
					/>
				) : null}
			</div>

			{truncated ? (
				// Said out loud rather than letting a capped list read as the whole
				// worktree: a file that is missing because of the cap looks identical to
				// one that does not exist.
				<p className="border-t border-border px-2 py-1 text-micro text-muted-foreground">
					Showing part of a large worktree — type more to narrow it.
				</p>
			) : null}
		</div>
	);
}

function displayBadge(badge?: string): string | undefined {
	if (!badge || badge.toLowerCase() === "agent") return undefined;
	return badge.toLowerCase() === "ao" ? "AO" : badge;
}
