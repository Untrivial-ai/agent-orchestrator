/**
 * Images in agent prose.
 *
 * A bare `<img className="max-w-full">` rendered a screenshot as wide as the
 * conversation and as tall as the image itself, so one image could push the
 * reply off screen, and there was no way to see it larger. Images now read the
 * way attachments on a human message do: a height-capped preview that opens at
 * full size in a dialog.
 *
 * A paragraph made only of images (`![a](u1) ![b](u2)`, or one per line) is a
 * set, not prose, so it becomes a wrapping row of equal-height thumbnails.
 * Images mixed into a sentence stay in the sentence.
 */

import { createContext, useContext, useState, type ReactNode } from "react";
import type { ExtraProps } from "react-markdown";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";
import { Dialog, DialogContent, DialogTitle } from "../ui/dialog";

/** Thumbnails in a gallery share one height so the row reads as a set. */
const InGallery = createContext(false);
/** A linked image already has an action; a button inside a link is invalid. */
const InLink = createContext(false);

/**
 * True when a paragraph holds two or more images and nothing else but the
 * whitespace and line breaks between them.
 */
export function isImageOnlyParagraph(node: ExtraProps["node"]): boolean {
	if (!node) return false;
	let images = 0;
	for (const child of node.children) {
		if (child.type === "element" && child.tagName === "img") images++;
		else if (child.type === "element" && child.tagName === "br") continue;
		else if (!(child.type === "text" && child.value.trim() === "")) return false;
	}
	return images >= 2;
}

export function ChatImageGallery({ children }: { children: ReactNode }) {
	const { t } = useTranslation();
	return (
		<div role="group" aria-label={t("chat.image.gallery")} className="my-2 flex flex-wrap gap-2 first:mt-0 last:mb-0">
			<InGallery.Provider value={true}>{children}</InGallery.Provider>
		</div>
	);
}

export function ChatImageLinkScope({ children }: { children: ReactNode }) {
	return <InLink.Provider value={true}>{children}</InLink.Provider>;
}

/**
 * react-markdown's `img` override for chat.
 *
 * A source that fails falls back to its alt text rather than a broken-image box,
 * matching `MarkdownImage` in the file viewer.
 */
export function ChatImage({ src, alt }: { src?: string | Blob; alt?: string }) {
	const { t } = useTranslation();
	const inGallery = useContext(InGallery);
	const inLink = useContext(InLink);
	const [open, setOpen] = useState(false);
	// The failed URL rather than a boolean: a new URL deserves its own attempt.
	const [failedSrc, setFailedSrc] = useState<string | null>(null);
	const url = typeof src === "string" ? src : undefined;
	const label = alt ?? "";

	if (!url || url === failedSrc) return <span className="text-muted-foreground">{label}</span>;

	const onError = () => setFailedSrc(url);

	if (inLink) {
		return (
			<img
				src={url}
				alt={label}
				loading="lazy"
				onError={onError}
				className="inline-block h-auto max-h-80 max-w-full rounded-md object-contain align-middle"
			/>
		);
	}

	return (
		<>
			<button
				type="button"
				onClick={() => setOpen(true)}
				aria-label={label ? t("chat.image.open", { name: label }) : t("chat.image.openUnnamed")}
				className="inline-block max-w-full cursor-zoom-in overflow-hidden rounded-md border border-border bg-background align-top transition-opacity duration-150 hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none"
			>
				<img
					src={url}
					alt={label}
					loading="lazy"
					onError={onError}
					className={cn(
						"block max-w-full",
						inGallery ? "h-40 w-auto object-cover" : "h-auto max-h-80 object-contain",
					)}
				/>
			</button>
			<Dialog open={open} onOpenChange={setOpen}>
				<DialogContent
					aria-describedby={undefined}
					className="z-overlay w-auto max-w-[calc(100vw-4rem)] gap-0 bg-popover p-2 pt-10"
				>
					<DialogTitle className="sr-only">{label || t("chat.image.untitled")}</DialogTitle>
					<img
						src={url}
						alt={label}
						className="block max-h-[calc(100svh-8rem)] max-w-full rounded-md object-contain"
					/>
				</DialogContent>
			</Dialog>
		</>
	);
}
