import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";
import { bareFieldClass, inputBaseClass } from "./input";

/**
 * The multiline member of the input family. It shares the base and the field
 * box with `Input` so a one-line and a multiline field are the same control to
 * the eye; only the vertical padding and the absence of a fixed height differ,
 * because a textarea owns its own height.
 */
const textareaVariants = cva(inputBaseClass, {
	variants: {
		variant: {
			field: "rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-2.5 py-2.5",
			bare: bareFieldClass,
		},
	},
	defaultVariants: { variant: "field" },
});

export const Textarea = React.forwardRef<
	HTMLTextAreaElement,
	React.TextareaHTMLAttributes<HTMLTextAreaElement> & VariantProps<typeof textareaVariants>
>(({ className, variant, ...props }, ref) => (
	<textarea data-slot="textarea" className={cn(textareaVariants({ variant }), className)} ref={ref} {...props} />
));

Textarea.displayName = "Textarea";
