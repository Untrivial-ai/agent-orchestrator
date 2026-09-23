interface PRBadgeProps {
	url: string;
}

export function PRBadge({ url }: PRBadgeProps) {
	const prNumber = url.match(/\/pull\/(\d+)/)?.[1];

	return (
		<a
			href={url}
			aria-label={`Pull request #${prNumber}`}
			className="font-mono text-[0.8em] font-normal text-muted-foreground underline decoration-muted-foreground/50 underline-offset-4 transition-colors hover:text-foreground hover:decoration-foreground"
		>
			#{prNumber}
		</a>
	);
}
