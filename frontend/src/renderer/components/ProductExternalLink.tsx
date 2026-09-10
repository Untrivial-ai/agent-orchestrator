import type { ExternalLinkProps } from "@aoagents/product-ui";

export function ProductExternalLink({
	ariaLabel,
	children,
	stopPropagation,
	...props
}: ExternalLinkProps) {
	return (
		<a
			{...props}
			aria-label={ariaLabel}
			onClick={stopPropagation ? (event) => event.stopPropagation() : undefined}
			onPointerDown={stopPropagation ? (event) => event.stopPropagation() : undefined}
			rel="noopener noreferrer"
			target="_blank"
		>
			{children}
		</a>
	);
}
