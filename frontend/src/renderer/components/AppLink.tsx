import { createContext, useContext, type ComponentProps } from "react";
import { Copy, ExternalLink, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../lib/bridge";
import { isWebLink, openLinkInSystemBrowser } from "../lib/external-link-policy";
import {
	ContextMenu,
	ContextMenuTrigger,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuSeparator,
} from "./ui/context-menu";

export const AppBrowserLinkContext = createContext<((url: string) => void) | undefined>(undefined);

/** Shared web-link behavior; native fragments and special schemes retain their handlers. */
export function AppLink({ href, onClick, onBrowserOpen, ...props }: ComponentProps<"a"> & {
	onBrowserOpen?: (url: string) => void;
}) {
	const { t } = useTranslation();
	const sessionBrowserOpen = useContext(AppBrowserLinkContext);
	const openBrowser = onBrowserOpen ?? sessionBrowserOpen;
	const webLink = !!href && isWebLink(href);
	const anchor = (
		<a
			{...props}
			href={href}
			onClick={(event) => {
				onClick?.(event);
				if (event.defaultPrevented || !href || !webLink) return;
				event.preventDefault();
				if (openBrowser && !event.ctrlKey && !event.metaKey && !event.altKey) openBrowser(href);
				else void openLinkInSystemBrowser(href);
			}}
		/>
	);
	if (!href || href.startsWith("#") || href.startsWith("/")) return anchor;
	return (
		<ContextMenu>
			<ContextMenuTrigger asChild>{anchor}</ContextMenuTrigger>
			<ContextMenuContent className="min-w-52">
				{webLink && (
					<>
						<ContextMenuItem disabled={!openBrowser} onSelect={() => openBrowser?.(href)}>
							<Globe aria-hidden="true" />
							{t("link.openInAOBrowser")}
						</ContextMenuItem>
						<ContextMenuItem onSelect={() => void openLinkInSystemBrowser(href)}>
							<ExternalLink aria-hidden="true" />
							{t("link.openInExternalBrowser")}
						</ContextMenuItem>
						<ContextMenuSeparator />
					</>
				)}
				<ContextMenuItem onSelect={() => void aoBridge.clipboard.writeText(href)}>
					<Copy aria-hidden="true" />
					{t("link.copy")}
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
}
