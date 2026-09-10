// Shared visual recipe for Radix action menus. Keep context and dropdown menus
// on this one surface so a menu opened with a right-click cannot drift from the
// equivalent trigger menu.
export const actionMenuContentClass =
	"z-overlay min-w-[10rem] overflow-hidden rounded-lg border border-border bg-card p-1 text-popover-foreground flex flex-col gap-px";

export const actionMenuItemClass =
	"relative flex cursor-default select-none items-center gap-2.5 rounded-md px-2 py-1.5 text-control outline-none transition-col text-muted-foreground focus:bg-interactive-hover focus:text-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:size-icon-lg [&_svg]:shrink-0 [&_svg]:text-muted-foreground focus:[&_svg]:text-foreground";

export const actionMenuLabelClass = "px-2 py-1.5 text-micro tracking-wide text-passive";
export const actionMenuSeparatorClass = "-mx-1 my-1 h-px bg-border";
