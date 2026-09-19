interface MediaPlaceholderProps {
	id: string;
	kind?: "gif" | "screenshot" | "video";
	caption: string;
	note: string;
}

/**
 * Visible slot in a weekly changelog draft where a human records a GIF,
 * screenshot, or short video before publish. Replace with a normal image or
 * <Video /> once the asset lands under public/changelog/<slug>/.
 */
export function MediaPlaceholder({
	id,
	kind = "gif",
	caption,
	note,
}: MediaPlaceholderProps) {
	return (
		<aside
			data-changelog-media-placeholder={id}
			data-kind={kind}
			className="not-prose my-8 rounded-xl border border-dashed border-orange-500/50 bg-orange-500/5 px-5 py-4"
		>
			<p className="text-xs font-mono uppercase tracking-[0.5px] text-orange-400 mb-2">
				Media placeholder · {kind} · {id}
			</p>
			<p className="text-sm font-medium text-foreground mb-1">{caption}</p>
			<p className="text-sm text-muted-foreground leading-relaxed whitespace-pre-wrap">
				{note}
			</p>
		</aside>
	);
}
