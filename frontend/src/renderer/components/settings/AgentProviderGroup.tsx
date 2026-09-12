import { ChevronDown } from "lucide-react";
import { useId, type ReactNode } from "react";
import { AgentAvatar } from "../AgentAvatar";

/**
 * Shared settings container for one agent provider. Provider identity and
 * provider-level actions live in the header; account rows stay below.
 */
export function AgentProviderGroup({
	provider,
	name,
	summary,
	action,
	expanded,
	onExpandedChange,
	collapseLocked = false,
	children,
}: {
	provider: string;
	name: string;
	summary?: string;
	action?: ReactNode;
	expanded: boolean;
	onExpandedChange: (expanded: boolean) => void;
	collapseLocked?: boolean;
	children: ReactNode;
}) {
	const headingId = useId();
	const contentId = useId();

	return (
		<section
			aria-labelledby={headingId}
			className="overflow-hidden rounded-md border border-border bg-[var(--color-bg-settings-row)]"
			data-agent-provider={provider}
		>
			<header className="flex min-h-16 items-center justify-between gap-4 px-4 py-3">
				<button
					type="button"
					aria-controls={contentId}
					aria-expanded={expanded}
					className="flex min-w-0 flex-1 items-center gap-3 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default"
					disabled={collapseLocked}
					onClick={() => onExpandedChange(!expanded)}
				>
					<AgentAvatar className="size-8 shrink-0" decorative provider={provider} />
					<div className="min-w-0">
						<span id={headingId} className="block truncate text-sm font-medium text-foreground">{name}</span>
						{summary ? <p className="mt-0.5 text-xs text-muted-foreground">{summary}</p> : null}
					</div>
					<ChevronDown
						aria-hidden="true"
						className={`ml-auto size-4 shrink-0 text-muted-foreground transition-transform ${expanded ? "" : "-rotate-90"}`}
					/>
				</button>
				{action ? <div className="shrink-0">{action}</div> : null}
			</header>
			{expanded ? <div id={contentId} className="border-t border-border">{children}</div> : null}
		</section>
	);
}
