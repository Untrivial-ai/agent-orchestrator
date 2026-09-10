import * as React from "react";
import { cn } from "../../lib/utils";

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
	({ className, type = "text", ...props }, ref) => (
		<input
			data-slot="input"
			className={cn(
				"h-control-form w-full min-w-0 rounded-md border border-transparent bg-input/50 px-3 py-1 text-sm text-foreground transition-[color,box-shadow,background-color] outline-none placeholder:text-muted-foreground focus-visible:outline-none disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50",
				className,
			)}
			ref={ref}
			type={type}
			{...props}
		/>
	),
);

Input.displayName = "Input";
