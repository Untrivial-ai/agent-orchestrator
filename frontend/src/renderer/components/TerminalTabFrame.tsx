import type { ButtonHTMLAttributes, MouseEvent as ReactMouseEvent, ReactNode, Ref } from "react";
import { cn } from "../lib/utils";

type TerminalTabFrameProps = {
	active: boolean;
	action?: ReactNode;
	/** overlay = absolute over the label (close buttons). inline = reserves width so status never covers the title. */
	actionLayout?: "overlay" | "inline";
	actionPosition?: "leading" | "trailing";
	trailingAction?: ReactNode;
	buttonRef?: Ref<HTMLButtonElement>;
	children: ReactNode;
	className?: string;
	contentClassName?: string;
	editingContent?: ReactNode;
	buttonProps?: ButtonHTMLAttributes<HTMLButtonElement>;
	"data-terminal-role"?: string;
};

// One shared chrome for agent, reviewer, and shell tabs. Keeping the divider,
// active surface, overflow action, and selection indicator in one composer
// prevents the tab families from drifting apart.
export function TerminalTabFrame({
	active,
	action,
	actionLayout = "overlay",
	actionPosition = "trailing",
	trailingAction,
	buttonRef,
	children,
	className,
	contentClassName,
	editingContent,
	buttonProps,
	"data-terminal-role": terminalRole,
}: TerminalTabFrameProps) {
	const { className: buttonClassName, ...restButtonProps } = buttonProps ?? {};
	const overlayTrailing =
		actionLayout === "overlay" && ((actionPosition === "trailing" && action) || trailingAction);
	const inlineAction =
		action && actionLayout === "inline" ? (
			<div
				className={cn(
					"flex shrink-0 items-center self-stretch",
					actionPosition === "leading" ? "pl-1" : "pr-1",
				)}
				data-terminal-tab-action
			>
				{action}
			</div>
		) : null;
	return (
		<span
			className={cn(
				"group relative inline-flex h-full shrink-0 self-stretch items-stretch border-r border-border",
				active ? "bg-overlay text-foreground" : "text-passive hover:bg-raised hover:text-foreground",
				className,
			)}
			data-terminal-role={terminalRole}
			data-terminal-tab-frame
			onClick={(event) => {
				if (event.target !== event.currentTarget) return;
				restButtonProps.onClick?.(event as unknown as ReactMouseEvent<HTMLButtonElement>);
			}}
		>
			<span className="relative inline-flex h-[calc(100%-2px)] min-w-0 flex-1 self-stretch">
				{actionPosition === "leading" ? inlineAction : null}
				{editingContent ?? (
					<button
						ref={buttonRef}
						className={cn(
							"inline-flex h-full min-w-0 flex-1 cursor-pointer items-center px-2 text-left text-control leading-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50",
							overlayTrailing && "pr-9",
							buttonClassName,
						)}
						{...restButtonProps}
					>
						<span className={cn("inline-flex min-w-0 items-center gap-2", contentClassName)}>
							{children}
						</span>
					</button>
				)}
				{actionPosition === "trailing" ? inlineAction : null}
				{action && actionLayout === "overlay" ? (
					<div
						className={cn(
							"absolute inset-y-0 z-20 flex items-center",
							actionPosition === "leading" ? "left-2" : "right-1",
						)}
						data-terminal-tab-action
					>
						{action}
					</div>
				) : null}
				{trailingAction ? (
					<div
						className="absolute inset-y-0 right-1 z-10 flex items-center"
						data-terminal-tab-action
					>
						{trailingAction}
					</div>
				) : null}
			</span>
			{active ? (
				<span
					aria-hidden="true"
					className="pointer-events-none absolute inset-x-0 bottom-0 h-0.5 bg-foreground/80"
					data-testid="active-terminal-tab-indicator"
				/>
			) : null}
		</span>
	);
}
