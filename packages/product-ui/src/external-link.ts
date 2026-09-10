import type { ComponentType, ReactNode } from "react";

export type ExternalLinkProps = {
	ariaLabel?: string;
	children: ReactNode;
	className?: string;
	href: string;
	stopPropagation?: boolean;
	title?: string;
};

export type ExternalLinkComponent = ComponentType<ExternalLinkProps>;
