import { ChevronRight } from "lucide-react";
import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui";
import { cn } from "../../lib/utils";
import { composeMenuCloseAutoFocus, useMenuReturnTarget } from "./menu-focus";
import {
	actionMenuContentClass,
	actionMenuItemClass,
	actionMenuLabelClass,
	actionMenuSeparatorClass,
} from "./menu-styles";

export const DropdownMenu = DropdownMenuPrimitive.Root;
export const DropdownMenuTrigger = DropdownMenuPrimitive.Trigger;
export const DropdownMenuGroup = DropdownMenuPrimitive.Group;
export const DropdownMenuPortal = DropdownMenuPrimitive.Portal;

export function DropdownMenuContent({
	className,
	onCloseAutoFocus,
	portalContainer,
	sideOffset = 6,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Content> & {
	portalContainer?: React.ComponentProps<typeof DropdownMenuPrimitive.Portal>["container"];
}) {
	const rememberReturnTarget = useMenuReturnTarget<HTMLDivElement>();
	return (
		<DropdownMenuPrimitive.Portal container={portalContainer}>
			<DropdownMenuPrimitive.Content
				ref={rememberReturnTarget}
				onCloseAutoFocus={composeMenuCloseAutoFocus(onCloseAutoFocus)}
				sideOffset={sideOffset}
				className={cn(
					actionMenuContentClass,
					"origin-(--radix-dropdown-menu-content-transform-origin)",
					"data-[state=open]:animate-popover-in data-[state=closed]:animate-popover-out",
					className,
				)}
				{...props}
			/>
		</DropdownMenuPrimitive.Portal>
	);
}

export function DropdownMenuItem({
	className,
	inset,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Item> & { inset?: boolean }) {
	return (
		<DropdownMenuPrimitive.Item
			className={cn(
				actionMenuItemClass,
				inset && "pl-8",
				className,
			)}
			{...props}
		/>
	);
}

export function DropdownMenuLabel({
	className,
	inset,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Label> & { inset?: boolean }) {
	return (
		<DropdownMenuPrimitive.Label
			className={cn(
				actionMenuLabelClass,
				inset && "pl-8",
				className,
			)}
			{...props}
		/>
	);
}

export function DropdownMenuSeparator({
	className,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Separator>) {
	return <DropdownMenuPrimitive.Separator className={cn(actionMenuSeparatorClass, className)} {...props} />;
}

export function DropdownMenuShortcut({ className, ...props }: React.ComponentProps<"span">) {
	return <span className={cn("ml-auto text-micro tracking-wide-md text-passive", className)} {...props} />;
}

export const DropdownMenuSub = DropdownMenuPrimitive.Sub;

/** A row that opens a side submenu; same row styling as DropdownMenuItem. */
export function DropdownMenuSubTrigger({
	className,
	children,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.SubTrigger>) {
	return (
		<DropdownMenuPrimitive.SubTrigger
			className={cn(actionMenuItemClass, "data-[state=open]:bg-interactive-hover", className)}
			{...props}
		>
			{children}
			<ChevronRight aria-hidden="true" className="ml-auto size-3.5 shrink-0 text-passive" />
		</DropdownMenuPrimitive.SubTrigger>
	);
}

/** The side panel a DropdownMenuSubTrigger opens; same surface as DropdownMenuContent. */
export function DropdownMenuSubContent({
	className,
	// Offsets are measured from the sub-trigger row, which sits inside the parent
	// menu's 1px border + 4px padding: 9 leaves a visible 4px gap instead of the
	// panels touching, and -5 lines the submenu's first row up with the trigger.
	sideOffset = 9,
	alignOffset = -5,
	...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.SubContent>) {
	return (
		<DropdownMenuPrimitive.Portal>
			<DropdownMenuPrimitive.SubContent
				alignOffset={alignOffset}
				sideOffset={sideOffset}
				className={cn(
					actionMenuContentClass,
					"origin-(--radix-dropdown-menu-content-transform-origin)",
					"data-[state=open]:animate-popover-in data-[state=closed]:animate-popover-out",
					className,
				)}
				{...props}
			/>
		</DropdownMenuPrimitive.Portal>
	);
}
