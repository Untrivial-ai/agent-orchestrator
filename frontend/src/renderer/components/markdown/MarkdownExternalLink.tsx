import type { ReactNode } from "react";
import { AppLink } from "../AppLink";
import { openLinkInSystemBrowser } from "../../lib/external-link-policy";

export function MarkdownExternalLink({ href, children }: { href: string; children?: ReactNode }) {
	return <AppLink href={href} target="_blank" rel="noreferrer noopener" onClick={(event) => {
		if (href.startsWith("mailto:")) {
			event.preventDefault();
			void openLinkInSystemBrowser(href);
		}
	}}>{children}</AppLink>;
}
