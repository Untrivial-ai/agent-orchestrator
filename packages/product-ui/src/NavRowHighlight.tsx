import { cn } from "./utils";

/**
 * Host class for rows that use {@link NavRowHighlight}.
 *
 * MUST keep host backgrounds transparent on hover/active/focus — the growing
 * pill owns the fill. Painting `hover:bg-interactive-hover` on the host again
 * doubles the highlight (sidebar + home recent-row regression).
 */
export const NAV_ROW_HIGHLIGHT_HOST_CLASS =
	"group/nav-row relative hover:bg-transparent! focus-visible:bg-transparent! active:bg-transparent! data-[active=true]:bg-transparent! hover:text-foreground data-[active=true]:font-medium data-[active=true]:text-foreground";

/**
 * Growing fill behind nav / home recent rows and inspector section accordions.
 *
 * Fine-pointer hover is gated in `styles.css` (`[data-nav-row-highlight-idle]`).
 */
export function NavRowHighlight({
	active = false,
	disabled = false,
}: {
	active?: boolean;
	disabled?: boolean;
}) {
	return (
		<span
			aria-hidden="true"
			className={cn(
				"pointer-events-none absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 rounded-lg bg-interactive-hover",
				"transition-[width,height] duration-normal ease-[var(--ease-out)]",
				"motion-reduce:h-full motion-reduce:w-full motion-reduce:transition-none",
				active
					? "h-full w-full bg-interactive-active opacity-100"
					: "h-[calc(100%-8px)] w-[calc(100%-8px)] opacity-0",
				disabled && !active && "opacity-0!",
			)}
			data-nav-row-highlight=""
			data-nav-row-highlight-idle={active || disabled ? undefined : ""}
		/>
	);
}
