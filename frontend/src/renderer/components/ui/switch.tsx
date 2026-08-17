"use client";

import * as React from "react";
import { Switch as SwitchPrimitive } from "radix-ui";

import { cn } from "@/lib/utils";

type SwitchProps = React.ComponentProps<typeof SwitchPrimitive.Root> & {
	size?: "default" | "sm";
};

function Switch({ className, size = "default", ...props }: SwitchProps) {
	return (
		<SwitchPrimitive.Root
			data-slot="switch"
			className={cn(
				"peer relative inline-flex shrink-0 cursor-pointer items-center rounded-full border-transparent transition-[background-color,border-color,box-shadow] outline-none after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=unchecked]:bg-input/90",
				size === "sm" ? "h-4 w-8 border" : "h-5 w-11 border-2",
				className,
			)}
			{...props}
		>
			<SwitchPrimitive.Thumb
				data-slot="switch-thumb"
				className={cn(
					"pointer-events-none block rounded-full bg-background shadow-sm ring-0 transition-transform data-[state=unchecked]:translate-x-0 dark:data-[state=checked]:bg-primary-foreground dark:data-[state=unchecked]:bg-foreground",
					size === "sm"
						? "size-3 data-[state=checked]:translate-x-4"
						: "h-4 w-6 data-[state=checked]:translate-x-[calc(100%-8px)]",
				)}
			/>
		</SwitchPrimitive.Root>
	);
}

export { Switch };
