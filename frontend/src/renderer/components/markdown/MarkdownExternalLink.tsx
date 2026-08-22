import type { ReactNode } from "react";
import { openLinkInSystemBrowser } from "../../lib/external-link-policy";

/**
 * A genuine http(s)/mailto link inside rendered markdown. There is no in-app
 * "AO Browser" concept in the Files tab (unlike chat's `ChatMarkdown`), so every
 * click — plain or modified — goes straight to the system browser/mail client.
 */
export function MarkdownExternalLink({ href, children }: { href: string; children?: ReactNode }) {
	return (
		<a
			href={href}
			target="_blank"
			rel="noreferrer noopener"
			onClick={(event) => {
				event.preventDefault();
				void openLinkInSystemBrowser(href);
			}}
		>
			{children}
		</a>
	);
}
