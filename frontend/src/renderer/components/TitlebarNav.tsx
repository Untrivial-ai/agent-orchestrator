import { useCanGoBack, useRouter } from "@tanstack/react-router";
import { ArrowLeft, ArrowRight, PanelLeft } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { sidebarIsVisible, useUiStore } from "../stores/ui-store";
import { AOMascot } from "./AOMascot";
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

// The brand appears only once the sidebar has fully slid open and disappears
// the moment it starts closing. The sidebar animates with
// a spring (no transitionend), so watch its container reach x = 0.
const SETTLE_TIMEOUT_MS = 1500;

function useSidebarSettledOpen(isSidebarOpen: boolean): boolean {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (!isSidebarOpen) {
      setSettled(false);
      return;
    }
    const started = performance.now();
    let frame = 0;
    const check = () => {
      const el = document.querySelector<HTMLElement>(
        '[data-slot="sidebar-container"]',
      );
      const open = el ? el.getBoundingClientRect().left >= -0.5 : false;
      if (open || performance.now() - started > SETTLE_TIMEOUT_MS) {
        setSettled(true);
        return;
      }
      frame = requestAnimationFrame(check);
    };
    frame = requestAnimationFrame(check);
    return () => cancelAnimationFrame(frame);
  }, [isSidebarOpen]);
  return isSidebarOpen && settled;
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
  const showBrand = useSidebarSettledOpen(isSidebarOpen);
  // The sidebar's minimum width is measured from the brand label, which only
  // exists once this mounts it. Nudge the sidebar's resize re-clamp so a stored
  // or default width narrower than the label grows to fit it.
  useEffect(() => {
    if (showBrand) window.dispatchEvent(new Event("resize"));
  }, [showBrand]);

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
  const topClass = isMac ? "top-px" : "top-0.75";
  const heightClass = "h-traffic-light-clearance";

  // History controls stay in the fixed cluster in both sidebar states. The
  // brand follows them and stays part of the window-drag region.
  // The brand sits in this fixed row, not inside the sidebar, so it does not
  // shrink when a small window caps the sidebar narrower than the label. Cap it
  // to the sidebar's right edge so it truncates instead of overlapping the tabs.
  const brandRef = useRef<HTMLSpanElement>(null);
  const [brandMaxWidth, setBrandMaxWidth] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!showBrand) return;
    const sidebar = document.querySelector<HTMLElement>('[data-slot="sidebar-container"]');
    if (!sidebar) return;
    const fit = () => {
      const brand = brandRef.current;
      if (!brand) return;
      const available = sidebar.getBoundingClientRect().right - brand.getBoundingClientRect().left - 12;
      setBrandMaxWidth(Math.max(24, Math.floor(available)));
    };
    fit();
    const observer = new ResizeObserver(fit);
    observer.observe(sidebar);
    window.addEventListener("resize", fit);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", fit);
    };
  }, [showBrand]);

  return (
    <div
      className={`fixed ${topClass} ${leftClass} z-titlebar flex ${heightClass} items-center gap-1`}
      data-slot="titlebar-nav"
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
      {showBrand ? (
        // Not a button on purpose: it stays part of the window-drag region.
        <span
          className="inline-flex select-none items-center gap-1.5 whitespace-nowrap text-base font-semibold leading-tight tracking-tight-lg text-foreground"
          data-sidebar-brand=""
          ref={brandRef}
          style={{ maxWidth: brandMaxWidth }}
        >
          <AOMascot className="h-5.5 w-5.5 shrink-0 -translate-y-px" />
          <span className="min-w-0 truncate" data-brand-label="">Orchestrator.inc</span>
        </span>
      ) : null}
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
