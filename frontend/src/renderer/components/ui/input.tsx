import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

/**
 * One input for the whole app. `default` is the standalone field; `bare` drops
 * every piece of chrome so a composite that owns the surface — the settings
 * search field, the inline row editor — can put its own border and focus ring
 * around it instead of overriding five utilities at the call site.
 */
export const inputBaseClass =
	"w-full min-w-0 text-sm text-foreground transition-[color,box-shadow,background-color] placeholder:text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50";

/** The bordered field's box, shared with `Textarea` so both draw one field. */
const fieldChrome = "rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-2.5";

export const bareFieldClass = "border-0 bg-transparent p-0 focus-visible:outline-none";

const inputVariants = cva(
	inputBaseClass,
	{
		variants: {
			variant: {
				default: "h-control-form rounded-md border border-transparent bg-input/50 px-3 py-1",
				/**
				 * The bordered settings field: the one filled input the app draws.
				 * Replaces the `menu-search-input`, `settings-field-control` and
				 * `settings-inline-input` utilities, which each spelled this chrome
				 * out again with slight differences.
				 */
				field: `h-(--size-settings-action-height) ${fieldChrome}`,
				bare: bareFieldClass,
			},
		},
		defaultVariants: { variant: "default" },
	},
);

export const Input = React.forwardRef<
	HTMLInputElement,
	React.InputHTMLAttributes<HTMLInputElement> & VariantProps<typeof inputVariants>
>(
	({ className, type = "text", variant, ...props }, ref) => (
		<input
			data-slot="input"
			className={cn(inputVariants({ variant }), className)}
			ref={ref}
			type={type}
			{...props}
		/>
	),
);

Input.displayName = "Input";
