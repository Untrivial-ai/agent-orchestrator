/**
 * Copy to clipboard, with the only feedback that matters: whether it worked.
 *
 * A denied clipboard says nothing rather than claiming "Copied" — a false
 * confirmation costs the user the text they thought they had. Shared by the code
 * block and the message action row so both confirm the same way.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

/** Long enough to be noticed, short enough that a second copy reads as a second copy. */
const CONFIRM_MS = 1400;

export function CopyButton({
	text,
	label,
	/** Icon-only, for places where a word of chrome would be one too many. */
	compact,
	className,
}: {
	text: string;
	label: string;
	compact?: boolean;
	className?: string;
}) {
	const [copied, setCopied] = useState(false);
	const timer = useRef<ReturnType<typeof setTimeout>>(undefined);

	useEffect(() => () => clearTimeout(timer.current), []);

	const copy = useCallback(() => {
		// Through the bridge rather than `navigator.clipboard`: in Electron that
		// reaches the native clipboard, which does not need the document to be focused
		// or a permission the renderer cannot prompt for.
		void aoBridge.clipboard.writeText(text).then(
			() => {
				setCopied(true);
				clearTimeout(timer.current);
				timer.current = setTimeout(() => setCopied(false), CONFIRM_MS);
			},
			() => {
				// Clipboard denied. Nothing to say that would not be a guess.
			},
		);
	}, [text]);

	const button = (
		<button
			type="button"
			onClick={copy}
			aria-label={copied ? "Copied" : label}
			className={cn(
				compact
					? "flex size-7 items-center justify-center rounded-md text-muted-foreground transition-[background-color,color,transform] hover:bg-interactive-hover hover:text-foreground"
					: "flex h-7 items-center gap-1 rounded-md px-2 text-[10.5px] text-muted-foreground transition-[background-color,color,transform] hover:bg-interactive-hover hover:text-foreground",
				className,
			)}
		>
			{copied ? (
				<Check aria-hidden="true" className="size-3 text-success" />
			) : (
				<Copy aria-hidden="true" className="size-3" />
			)}
			{compact ? null : copied ? "Copied" : "Copy"}
		</button>
	);

	// Icon-only leaves nothing on screen to explain itself, so the tooltip
	// carries the label there and only there.
	if (!compact) return button;

	return (
		<Tooltip>
			<TooltipTrigger asChild>{button}</TooltipTrigger>
			<TooltipContent side="bottom">{copied ? "Copied" : label}</TooltipContent>
		</Tooltip>
	);
}
