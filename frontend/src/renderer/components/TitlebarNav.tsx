import { useCanGoBack, useRouter } from "@tanstack/react-router";
import { ArrowLeft, ArrowRight, PanelLeft } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { useUiStore } from "../stores/ui-store";

const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();
const noDragStyle = isMac
  ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties)
  : undefined;

// Sidebar chrome cluster (sidebar toggle + history arrows). It stays fixed while
// the sidebar expands, collapses, or appears as a hover preview. macOS pins it
// beside the traffic lights; Linux has no traffic lights, so it sits at the
// sidebar's top-left. (Windows keeps these controls in its own titlebar.)
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
  hasSessionTopbar = false,
  isFullScreen = false,
  onSidebarPreviewEnter,
}: {
  historyLocked?: boolean;
  hasSessionTopbar?: boolean;
  isFullScreen?: boolean;
  onSidebarPreviewEnter?: React.PointerEventHandler<HTMLButtonElement>;
}) {
  const { t } = useTranslation();
  const { isSidebarOpen, toggleSidebar } = useUiStore();
  const router = useRouter();
  const canGoBack = useCanGoBack();
  const canGoForward = useCanGoForward();

  if (!isMac && !isLinux) return null;

  // macOS: pinned beside the traffic lights. Native dots sit at y: 12 with a
  // 12px hit target (centerline 18); the 40px clearance band is items-centered,
  // so top: -2px puts the toggle/arrows on that same centerline. Linux: no
  // traffic lights, so it sits at the sidebar's top-left within the reserved
  // titlebar band.
  const leftClass = !isMac
    ? "left-0"
    : isFullScreen
      ? "left-titlebar-cluster-left-fullscreen"
      : "left-titlebar-cluster-left";
  // Linux: match the framed board titlebar's y (mac inset 2px + surface border
  // 1px) so the cluster shares its centerline with the project title.
  const topClass = !isMac
    ? "top-0.75"
    : isFullScreen && hasSessionTopbar && !isSidebarOpen
      ? "top-1.5"
      : isFullScreen
        ? "top-0"
        : "-top-0.6";
  const heightClass =
    isMac && isFullScreen
      ? "h-traffic-light-clearance-fullscreen"
      : "h-traffic-light-clearance";

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
        onPointerEnter={onSidebarPreviewEnter}
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
  onPointerEnter,
  children,
}: {
  label: string;
  title: string;
  disabled?: boolean;
  tabIndex?: number;
  onClick: () => void;
  onPointerEnter?: React.PointerEventHandler<HTMLButtonElement>;
  children: React.ReactNode;
}) {
  return (
    <button
      aria-label={label}
      aria-disabled={disabled || undefined}
      className="grid size-control-md place-items-center rounded-md text-passive transition-colors hover:bg-interactive-hover hover:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-55 disabled:hover:bg-transparent disabled:hover:text-passive"
      disabled={disabled}
      onClick={onClick}
      onPointerEnter={onPointerEnter}
      style={noDragStyle}
      tabIndex={tabIndex}
      title={title}
      type="button"
    >
      {children}
    </button>
  );
}
