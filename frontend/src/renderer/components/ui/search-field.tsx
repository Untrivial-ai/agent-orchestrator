import { Search } from "lucide-react";
import type { Ref } from "react";
import { cn } from "../../lib/utils";
import { Input } from "./input";

/**
 * The app's one search input. Two contexts, one component:
 *
 * - `field` — a bordered field that owns its own surface (settings pages).
 * - `menu`  — a filter inside a menu or popover panel, where the panel is the
 *   surface, so all this draws is the icon and the input.
 *
 * Both own their focus cue, so the composite reads as one control to the eye
 * and to the keyboard instead of the input drawing a second ring inside it.
 */
export function SearchField({
	className,
	inputRef,
	label,
	onChange,
	placeholder,
	value,
	variant = "field",
}: {
	className?: string;
	/** Forwarded to the input, for menus that focus the filter on open. */
	inputRef?: Ref<HTMLInputElement>;
	/** Accessible name. A placeholder is an example, never a label. */
	label: string;
	onChange: (value: string) => void;
	placeholder: string;
	value: string;
	variant?: "field" | "menu";
}) {
	if (variant === "menu") {
		return (
			<div className={cn("relative", className)}>
				<Search
					aria-hidden="true"
					className="pointer-events-none absolute left-3 top-1/2 size-icon-sm -translate-y-1/2 text-muted-foreground"
				/>
				<Input
					aria-label={label}
					className="pl-8"
					onChange={(event) => onChange(event.target.value)}
					placeholder={placeholder}
					ref={inputRef}
					type="search"
					value={value}
					variant="field"
				/>
			</div>
		);
	}

	return (
		<label
			className={cn(
				"flex h-9 min-w-0 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3 focus-within:outline-2 focus-within:outline-offset-1 focus-within:outline-ring",
				className,
			)}
		>
			<Search aria-hidden="true" className="size-icon-base shrink-0 text-muted-foreground" />
			<Input
				aria-label={label}
				className="min-w-0 flex-1 text-foreground placeholder:text-muted-foreground"
				onChange={(event) => onChange(event.target.value)}
				placeholder={placeholder}
				ref={inputRef}
				type="search"
				value={value}
				variant="bare"
			/>
		</label>
	);
}
