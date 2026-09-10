import { FileText, Image as ImageIcon, Tag } from "lucide-react";
import { cn } from "../../lib/utils";
import type { ConversationContentSummary } from "../../types/conversation";

interface ConversationContentItem extends ConversationContentSummary {
	data?: string;
}

export function ConversationContentItems({
	content,
	ariaLabel,
	imageLabel,
	imageAlt,
	className,
}: {
	content: ConversationContentItem[];
	ariaLabel: string;
	imageLabel: string;
	imageAlt?: (position: number) => string;
	className?: string;
}) {
	if (content.length === 0) return null;

	let imagePosition = 0;
	return (
		<ul aria-label={ariaLabel} className={cn("flex max-w-full flex-wrap gap-1.5", className)}>
			{content.map((item, index) => {
				const previewableImage =
					item.type === "image" &&
					typeof item.data === "string" &&
					item.data.length > 0 &&
					typeof item.mimeType === "string" &&
					item.mimeType.toLowerCase().startsWith("image/");
				const key = `${item.type}-${item.uri ?? item.mimeType ?? item.name ?? "content"}-${index}`;

				if (previewableImage) {
					imagePosition += 1;
					return (
						<li key={key} className="max-w-full overflow-hidden rounded-md border border-border bg-background">
							<img
								src={`data:${item.mimeType};base64,${item.data}`}
								alt={imageAlt?.(imagePosition) ?? `${imageLabel} ${imagePosition}`}
								className="block h-auto max-h-80 max-w-full object-contain"
							/>
						</li>
					);
				}

				const Icon = item.type === "image" ? ImageIcon : item.type === "resource" ? FileText : Tag;
				const label = item.type === "image" ? item.mimeType || imageLabel : item.name || item.uri || item.type;
				return (
					<li
						key={key}
						title={item.uri || label}
						className="flex min-w-0 max-w-full items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground"
					>
						<Icon aria-hidden="true" className="size-3 shrink-0" />
						<span className="truncate">{label}</span>
					</li>
				);
			})}
		</ul>
	);
}
