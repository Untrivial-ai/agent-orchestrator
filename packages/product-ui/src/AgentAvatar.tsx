import type { HTMLAttributes, ImgHTMLAttributes } from "react";
import { getAgentIdentity } from "./agents";
import { cn } from "./utils";

export type AgentLogoSources = Readonly<Record<string, string | undefined>>;

export type AgentAvatarProps = {
	provider: string;
	className?: string;
	decorative?: boolean;
	logoSrc?: string;
	logoSources?: AgentLogoSources;
};

export function AgentAvatar({
	provider,
	className,
	decorative = false,
	logoSrc,
	logoSources,
}: AgentAvatarProps) {
	const identity = getAgentIdentity(provider);
	const logo = logoSrc ?? (identity.logoKey ? logoSources?.[identity.logoKey] : undefined);
	if (logo) {
		const imageAccessibility: Pick<ImgHTMLAttributes<HTMLImageElement>, "alt" | "aria-hidden" | "title"> =
			decorative
				? { alt: "", "aria-hidden": true }
				: { alt: provider, title: provider };
		return (
			<img
				{...imageAccessibility}
				className={cn("size-icon-xl shrink-0 object-contain", className)}
				draggable={false}
				src={logo}
			/>
		);
	}

	const fallbackAccessibility: Pick<HTMLAttributes<HTMLSpanElement>, "aria-hidden" | "aria-label" | "role" | "title"> =
		decorative
			? { "aria-hidden": true }
			: { "aria-label": provider, role: "img", title: provider };
	return (
		<span
			{...fallbackAccessibility}
			className={cn(
				"inline-flex size-icon-xl shrink-0 items-center justify-center text-caption font-bold uppercase leading-none text-muted-foreground",
				className,
			)}
		>
			{identity.initial}
		</span>
	);
}
