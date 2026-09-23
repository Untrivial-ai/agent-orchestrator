"use client";

import * as React from "react";
import { Check } from "lucide-react";
import { Checkbox as CheckboxPrimitive } from "radix-ui";

import { cn } from "../../lib/utils";

const Checkbox = React.forwardRef<
	React.ElementRef<typeof CheckboxPrimitive.Root>,
	React.ComponentPropsWithoutRef<typeof CheckboxPrimitive.Root>
>(({ className, ...props }, ref) => (
	<CheckboxPrimitive.Root
		ref={ref}
		className={cn(
			"grid size-4 shrink-0 place-items-center rounded-sm border border-border-strong bg-input/90 text-primary-foreground outline-none transition-[background-color,border-color,box-shadow,transform] duration-fast hover:border-primary/70 focus-visible:ring-2 focus-visible:ring-ring/60 active:scale-95 disabled:cursor-not-allowed disabled:opacity-60 data-[state=checked]:border-primary data-[state=checked]:bg-primary",
			className,
		)}
		{...props}
	>
		<CheckboxPrimitive.Indicator>
			<Check className="size-3 stroke-[2.5]" aria-hidden="true" />
		</CheckboxPrimitive.Indicator>
	</CheckboxPrimitive.Root>
));

Checkbox.displayName = "Checkbox";

export { Checkbox };
