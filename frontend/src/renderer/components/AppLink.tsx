import { createContext, useContext, useState, type ComponentProps } from "react";
import { Copy, ExternalLink, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../lib/bridge";
import { isWebLink, openLinkInSystemBrowser } from "../lib/external-link-policy";
import { useLinkPreview } from "../hooks/useLinkPreview";
import { LinkPreviewCard, LinkPreviewCardLoading } from "./LinkPreviewCard";
import {
	ContextMenu,
	ContextMenuTrigger,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuSeparator,
} from "./ui/context-menu";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "./ui/hover-card";

export const AppBrowserLinkContext = createContext<((url: string) => void) | undefined>(undefined);

/** Shared web-link behavior; native fragments and special schemes retain their handlers. */
export function AppLink({ href, onClick, onBrowserOpen, inAppLink, ...props }: ComponentProps<"a"> & {
	onBrowserOpen?: (url: string) => void;
	inAppLink?: (url: string) => boolean;
}) {
	const { t } = useTranslation();
	const sessionBrowserOpen = useContext(AppBrowserLinkContext);
	const openBrowser = onBrowserOpen ?? sessionBrowserOpen;
	const webLink = !!href && isWebLink(href);
	const browserLink = !!href && (inAppLink?.(href) ?? webLink);
	const [previewOpen, setPreviewOpen] = useState(false);
	const previewQuery = useLinkPreview(href ?? "", previewOpen && webLink);
	const anchor = (
		<a
			{...props}
			href={href}
			onClick={(event) => {
				onClick?.(event);
				if (event.defaultPrevented || !href || !browserLink) return;
				event.preventDefault();
				if (openBrowser && !event.ctrlKey && !event.metaKey && !event.altKey) openBrowser(href);
				else void openLinkInSystemBrowser(href);
			}}
		/>
	);
	if (!href || href.startsWith("#") || href.startsWith("/")) return anchor;
	const trigger = webLink ? (
		<ContextMenuTrigger asChild>
			<HoverCardTrigger asChild>{anchor}</HoverCardTrigger>
		</ContextMenuTrigger>
	) : (
		<ContextMenuTrigger asChild>{anchor}</ContextMenuTrigger>
	);
	const menu = (
		<ContextMenu>
			{trigger}
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
	if (!webLink) return menu;
	return (
		<HoverCard onOpenChange={setPreviewOpen}>
			{menu}
			{previewOpen && !previewQuery.isError && (
				<HoverCardContent collisionPadding={8}>
					{previewQuery.data ? (
						<LinkPreviewCard url={href} preview={previewQuery.data} />
					) : (
						<LinkPreviewCardLoading />
					)}
				</HoverCardContent>
			)}
		</HoverCard>
	);
}
