import { useCanGoBack, useRouter } from "@tanstack/react-router";
import { ArrowLeft, ArrowRight, PanelLeft } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { sidebarIsVisible, useUiStore } from "../stores/ui-store";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();
const noDragStyle = isMac
  ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties)
  : undefined;

// Sidebar chrome cluster (sidebar toggle + history arrows). It stays fixed while
// the sidebar expands or collapses. macOS pins it beside the traffic lights;
// Linux has no traffic lights, so it sits at the sidebar's top-left. (Windows
// keeps these controls in its own titlebar.)
// The installed router has no useCanGoForward, and deriving one as
// `__TSR_index < history.length - 1` (the upstream hook's approach) is wrong
// here: window.history.length also counts entries the router never created —
// the WebContents' initial blank entry, pre-router loads — so the tip of the
// stack still reads as "forward available" and the arrow no-ops. Instead,
// track the highest router index reachable on the live stack: a PUSH discards
// the forward entries (the new index is the tip); BACK/FORWARD/GO only move
// within it. After a mid-stack reload the tip resets to the current entry —
// forward greys out rather than dangle on entries we can no longer see.
export function useCanGoForward(): boolean {
  const router = useRouter();
  const [canGoForward, setCanGoForward] = useState(false);
  useEffect(() => {
    let tip = router.history.location.state.__TSR_index;
    return router.history.subscribe(({ location, action }) => {
      const index = location.state.__TSR_index;
      tip = action.type === "PUSH" ? index : Math.max(tip, index);
      setCanGoForward(index < tip);
    });
  }, [router]);
  return canGoForward;
}

export function TitlebarNav({
  historyLocked = false,
  isFullScreen = false,
}: {
  historyLocked?: boolean;
  isFullScreen?: boolean;
}) {
  const { t } = useTranslation();
  const toggleSidebar = useUiStore((state) => state.toggleSidebar);
  const isSidebarOpen = useUiStore(sidebarIsVisible);
  const router = useRouter();
  const canGoBack = useCanGoBack();
  const canGoForward = useCanGoForward();

  if (!isMac && !isLinux) return null;
  // Native fullscreen changes only the horizontal traffic-light reserve.
  // Sidebar and route state must never move the navigation centerline.
  const leftClass = !isMac
    ? isSidebarOpen
      ? "left-titlebar-cluster-left-linux"
      : "left-titlebar-cluster-left-linux-panel"
    : isFullScreen
      ? "left-titlebar-cluster-left-fullscreen"
      : "left-titlebar-cluster-left";
  const topClass = isMac ? "top-0" : "top-0.75";
  const heightClass = "h-traffic-light-clearance";

  return (
    <div
      className={`fixed ${topClass} ${leftClass} z-titlebar flex ${heightClass} items-center gap-1`}
      data-slot="titlebar-nav"
      style={noDragStyle}
    >
      <TitlebarButton
        label={
          isSidebarOpen ? t("shell.collapseSidebar") : t("shell.expandSidebar")
        }
        onClick={toggleSidebar}
        title={
          isSidebarOpen
            ? t("titlebar.collapseSidebarShortcut")
            : t("titlebar.expandSidebarShortcut")
        }
      >
        <PanelLeft className="size-icon-lg" aria-hidden="true" />
      </TitlebarButton>
      <TitlebarButton
        disabled={historyLocked || !canGoBack}
        label={t("titlebar.goBack")}
        onClick={() => router.history.back()}
        title={t("titlebar.goBack")}
      >
        <ArrowLeft className="size-icon-lg" aria-hidden="true" />
      </TitlebarButton>
      <TitlebarButton
        disabled={historyLocked || !canGoForward}
        label={t("titlebar.goForward")}
        onClick={() => router.history.forward()}
        title={t("titlebar.goForward")}
      >
        <ArrowRight className="size-icon-lg" aria-hidden="true" />
      </TitlebarButton>
    </div>
  );
}

function TitlebarButton({
  label,
  title,
  disabled,
  tabIndex,
  onClick,
  children,
}: {
  label: string;
  title: string;
  disabled?: boolean;
  tabIndex?: number;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">
          <button
            aria-label={label}
            aria-disabled={disabled || undefined}
            className="grid size-control-md place-items-center rounded-md text-passive transition-colors hover:bg-interactive-hover hover:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-55 disabled:hover:bg-transparent disabled:hover:text-passive"
            disabled={disabled}
            onClick={onClick}
            style={noDragStyle}
            tabIndex={tabIndex}
            type="button"
          >
            {children}
          </button>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom">{title}</TooltipContent>
    </Tooltip>
  );
}
