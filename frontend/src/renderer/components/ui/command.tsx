"use client";

import * as React from "react";
import { FocusScope } from "@radix-ui/react-focus-scope";
import { Command as CommandPrimitive } from "cmdk";
import * as Dialog from "@radix-ui/react-dialog";
import { Search } from "lucide-react";

import { cn } from "@/lib/utils";

function Command({ className, ...props }: React.ComponentProps<typeof CommandPrimitive>) {
	return (
		<CommandPrimitive
			data-slot="command"
			className={cn(
				"flex w-full flex-col overflow-hidden rounded-[var(--radius-command-palette)] bg-[var(--color-bg-command-palette)] text-[var(--color-text-command-item)] outline-none",
				className,
			)}
			{...props}
		/>
	);
}

function CommandDialog({
	title = "Command palette",
	description = "Search projects, sessions, PRs, and commands",
	children,
	className,
	commandProps,
	contentProps,
	modal,
	...props
}: React.ComponentProps<typeof Dialog.Root> & {
	title?: string;
	description?: string;
	className?: string;
	commandProps?: React.ComponentProps<typeof CommandPrimitive>;
	contentProps?: React.ComponentProps<typeof Dialog.Content>;
}) {
	const { className: commandClassName, ...restCommandProps } = commandProps ?? {};
	const nonModal = modal === false;
	const content = (
		<Dialog.Content
			data-slot="command-dialog-content"
			aria-label={title}
			aria-modal={nonModal || undefined}
			{...contentProps}
			className={cn(
				"fixed left-1/2 top-command-palette z-overlay w-command-palette -translate-x-1/2 overflow-hidden rounded-[var(--radius-command-palette)] border border-[var(--color-border-command-palette)] bg-[var(--color-bg-command-palette)] text-[var(--color-text-command-item)] shadow-[var(--shadow-command-palette)] outline-none data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none",
				className,
			)}
		>
			<Dialog.Title className="sr-only">{title}</Dialog.Title>
			<Dialog.Description className="sr-only">{description}</Dialog.Description>
			<Command
				className={cn(
					"**:[[cmdk-group-heading]]:px-[var(--size-command-pad-x)] **:[[cmdk-group-heading]]:pt-2.5 **:[[cmdk-group-heading]]:pb-1 **:[[cmdk-group-heading]]:text-[11px] **:[[cmdk-group-heading]]:font-normal **:[[cmdk-group-heading]]:tracking-wide **:[[cmdk-group-heading]]:text-[var(--color-text-command-muted)] **:[[cmdk-group]]:px-0",
					commandClassName,
				)}
				{...restCommandProps}
			>
				{children}
			</Command>
		</Dialog.Content>
	);
	return (
		<Dialog.Root data-slot="command-dialog" modal={modal} {...props}>
			<Dialog.Portal>
				{nonModal ? (
					<>
						<div
							data-slot="command-dialog-overlay"
							className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out motion-reduce:animate-none"
							onWheel={(event) => event.preventDefault()}
						/>
						<FocusScope loop trapped>{content}</FocusScope>
					</>
				) : (
					<>
						<Dialog.Overlay
							data-slot="command-dialog-overlay"
							className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out motion-reduce:animate-none"
						/>
						{content}
					</>
				)}
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function CommandInput({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Input>) {
	return (
		<div
			data-slot="command-input-wrapper"
			className="flex items-center gap-2.5 border-b border-[var(--color-border-command-palette)] px-[var(--size-command-pad-x)] py-3"
			cmdk-input-wrapper=""
		>
			<Search className="size-4 shrink-0 text-[var(--color-text-command-placeholder)]" aria-hidden="true" />
			<CommandPrimitive.Input
				data-slot="command-input"
				className={cn(
					"flex h-6 w-full bg-transparent text-[length:var(--font-size-command-input)] leading-6 text-[var(--color-text-command-item)] caret-[var(--color-text-command-item)] outline-none placeholder:text-[var(--color-text-command-placeholder)] disabled:cursor-not-allowed disabled:opacity-50",
					className,
				)}
				{...props}
			/>
		</div>
	);
}

function CommandList({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.List>) {
	return (
		<CommandPrimitive.List
			data-slot="command-list"
			className={cn(
				"max-h-command-palette-list scrollbar-none scroll-py-1 overflow-y-auto overflow-x-hidden overscroll-contain py-1 outline-none",
				className,
			)}
			{...props}
		/>
	);
}

function CommandEmpty({ ...props }: React.ComponentProps<typeof CommandPrimitive.Empty>) {
	return (
		<CommandPrimitive.Empty
			data-slot="command-empty"
			className="py-8 text-center text-sm text-[var(--color-text-command-muted)]"
			{...props}
		/>
	);
}

function CommandGroup({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Group>) {
	return (
		<CommandPrimitive.Group data-slot="command-group" className={cn("overflow-hidden pb-1", className)} {...props} />
	);
}

function CommandItem({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Item>) {
	return (
		<CommandPrimitive.Item
			data-slot="command-item"
			className={cn(
				"relative mx-[var(--size-command-item-inset)] flex cursor-default select-none items-center gap-2.5 rounded-[var(--radius-command-item)] py-1.5 pr-2.5 pl-[var(--size-command-item-pad-l)] text-[13px] leading-[length:var(--leading-command-item)] text-[var(--color-text-command-item)] outline-none",
				"data-[selected=true]:bg-[var(--color-bg-command-item-active)] data-[selected=true]:text-[var(--color-text-command-item)]",
				"data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50",
				"[&_svg]:size-3.5 [&_svg]:shrink-0 [&_svg]:text-[var(--color-text-command-item)] data-[selected=true]:[&_svg]:text-[var(--color-text-command-item)]",
				className,
			)}
			{...props}
		/>
	);
}

function CommandFooter({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="command-footer"
			className={cn(
				"flex items-center gap-4 border-t border-[var(--color-border-command-palette)] px-[var(--size-command-pad-x)] pt-3 pb-3 text-sm text-[var(--color-text-command-muted)]",
				className,
			)}
			{...props}
		/>
	);
}

export {
	Command,
	CommandDialog,
	CommandInput,
	CommandList,
	CommandEmpty,
	CommandGroup,
	CommandItem,
	CommandFooter,
};
