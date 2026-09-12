"use client";

import * as React from "react";
import { Switch as SwitchPrimitive } from "radix-ui";

import { cn } from "@/lib/utils";

/**
 * One switch for the whole app. Size is deliberately not a prop: the control
 * reads as the same affordance in a settings row and in a dense menu row, so a
 * per-call-site size only lets those surfaces drift apart.
 */
function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
	return (
		<SwitchPrimitive.Root
			data-slot="switch"
			className={cn(
				"peer relative inline-flex h-4 w-8 shrink-0 cursor-pointer items-center rounded-full border border-transparent transition-[transform,background-color,border-color,box-shadow] duration-fast ease-out active:scale-[0.96] outline-none after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:!cursor-pointer disabled:opacity-50 data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=unchecked]:bg-input/90",
				className,
			)}
			{...props}
		>
			<SwitchPrimitive.Thumb
				data-slot="switch-thumb"
				className="pointer-events-none block size-3 rounded-full bg-background shadow-sm ring-0 transition-transform duration-fast ease-out data-[state=unchecked]:translate-x-0.5 data-[state=checked]:translate-x-4 dark:data-[state=checked]:bg-primary-foreground dark:data-[state=unchecked]:bg-foreground"
			/>
		</SwitchPrimitive.Root>
	);
}

export { Switch };
