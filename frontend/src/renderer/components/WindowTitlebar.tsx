import { useCanGoBack, useRouter } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
  ArrowLeft,
  ArrowRight,
  Copy,
  Minus,
  PanelLeft,
  Square,
  X,
} from "lucide-react";
import { useEffect, useState } from "react";
import { sidebarIsVisible, useUiStore } from "../stores/ui-store";
import { useCanGoForward } from "./TitlebarNav";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

// Windows-only: macOS keeps its system menu bar and inset traffic lights; Linux
// keeps the existing minimal chrome. Only Windows loses the native title bar and
// needs the app to paint its own (see the win32 branch in main.ts).
const isWindows =
  typeof navigator !== "undefined" &&
  /win/i.test(
    (navigator as Navigator & { userAgentData?: { platform?: string } })
      .userAgentData?.platform ??
      navigator.platform ??
      "",
  );

type MenuKey = "view" | "help";

// Dispatch a native-menu action to the main process (see menu:action in main.ts).
const act = (action: string) => () => {
  void window.ao?.menu?.action(action);
};

// One top-level menu (View/Help). Declared at module scope, not inside
// WindowTitlebar, so React keeps it mounted across renders and the open dropdown
// doesn't reset while `openMenu` state changes.
function TopMenu({
  id,
  label,
  openMenu,
  setOpenMenu,
  children,
}: {
  id: MenuKey;
  label: string;
  openMenu: MenuKey | null;
  setOpenMenu: (key: MenuKey | null) => void;
  children: React.ReactNode;
}) {
  return (
    // modal={false} so pointer events still reach the sibling triggers while a
    // menu is open — that's what lets hover switch between the visible menus.
    <DropdownMenu
      modal={false}
      open={openMenu === id}
      onOpenChange={(open) => setOpenMenu(open ? id : null)}
    >
      <DropdownMenuTrigger asChild>
        <button
          className="window-titlebar__menu-btn"
          data-active={openMenu === id ? "" : undefined}
          onMouseEnter={() => setOpenMenu(openMenu === null ? null : id)}
          type="button"
        >
          {label}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="window-titlebar__menu"
        data-browser-native-overlay="true"
        sideOffset={4}
      >
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function WindowControls({
  isMaximized,
  t,
}: {
  isMaximized: boolean;
  t: TFunction;
}) {
  return (
    <div
      aria-label={t("titlebar.window")}
      className="window-titlebar__controls"
      role="group"
    >
      <button
        aria-label={t("titlebar.minimize")}
        className="window-titlebar__control"
        onClick={act("window.minimize")}
        type="button"
      >
        <Minus aria-hidden="true" className="window-titlebar__control-icon" />
      </button>
      <button
        aria-label={t("titlebar.maximize")}
        className="window-titlebar__control"
        onClick={act("window.maximize")}
        type="button"
      >
        {isMaximized ? (
          <Copy
            aria-hidden="true"
            className="window-titlebar__control-icon window-titlebar__control-icon--maximize"
          />
        ) : (
          <Square
            aria-hidden="true"
            className="window-titlebar__control-icon window-titlebar__control-icon--maximize"
          />
        )}
      </button>
      <button
        aria-label={t("titlebar.close")}
        className="window-titlebar__control window-titlebar__control--close"
        onClick={act("window.close")}
        type="button"
      >
        <X aria-hidden="true" className="window-titlebar__control-icon" />
      </button>
    </div>
  );
}

export function WindowTitlebar() {
  const { t } = useTranslation();
  const toggleSidebar = useUiStore((state) => state.toggleSidebar);
  const isSidebarOpen = useUiStore(sidebarIsVisible);
  const router = useRouter();
  const canGoBack = useCanGoBack();
  const canGoForward = useCanGoForward();
  const [openMenu, setOpenMenu] = useState<MenuKey | null>(null);
  const [isMaximized, setIsMaximized] = useState(false);

  useEffect(() => {
    if (!isWindows) return;
    let active = true;
    void window.ao?.window?.isMaximized().then((maximized) => {
      if (active) setIsMaximized(maximized);
    });
    const unsubscribe = window.ao?.window?.onMaximized((maximized) =>
      setIsMaximized(maximized),
    );
    return () => {
      active = false;
      unsubscribe?.();
    };
  }, []);

  // Tell main when a non-browser shell surface is used. BrowserPanel reports
  // its own interactions separately; the titlebar menu intentionally preserves
  // the previous target so its actions still apply to the underlying panel.
  useEffect(() => {
    const onShellUse = (event: Event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[class*="window-titlebar"]')) return;
      if (target?.closest('[data-testid="browser-panel"]')) return;
      void window.ao?.menu?.notifyShellFocus();
    };
    document.addEventListener("focusin", onShellUse);
    document.addEventListener("pointerdown", onShellUse, true);
    return () => {
      document.removeEventListener("focusin", onShellUse);
      document.removeEventListener("pointerdown", onShellUse, true);
    };
  }, []);

  if (!isWindows) return null;

  return (
    <header className="window-titlebar">
      {/* Sidebar collapse toggle — same ui-store path as the macOS TitlebarNav
			    cluster, so it stays in sync with the SidebarProvider. The brand
			    logo + name stay in the sidebar header instead of duplicating here. */}
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            aria-label={
              isSidebarOpen
                ? t("shell.collapseSidebar")
                : t("shell.expandSidebar")
            }
            className="window-titlebar__toggle"
            onClick={toggleSidebar}
            type="button"
          >
            <PanelLeft
              aria-hidden="true"
              className="window-titlebar__toggle-icon"
            />
          </button>
        </TooltipTrigger>
        <TooltipContent side="bottom">
          {isSidebarOpen
            ? t("shell.collapseSidebarTitle")
            : t("shell.expandSidebarTitle")}
        </TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="inline-flex">
            <button
              aria-label={t("titlebar.goBack")}
              className="window-titlebar__toggle"
              disabled={!canGoBack}
              onClick={() => router.history.back()}
              type="button"
            >
              <ArrowLeft
                aria-hidden="true"
                className="window-titlebar__toggle-icon"
              />
            </button>
          </span>
        </TooltipTrigger>
        <TooltipContent side="bottom">{t("titlebar.goBack")}</TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="inline-flex">
            <button
              aria-label={t("titlebar.goForward")}
              className="window-titlebar__toggle"
              disabled={!canGoForward}
              onClick={() => router.history.forward()}
              type="button"
            >
              <ArrowRight
                aria-hidden="true"
                className="window-titlebar__toggle-icon"
              />
            </button>
          </span>
        </TooltipTrigger>
        <TooltipContent side="bottom">{t("titlebar.goForward")}</TooltipContent>
      </Tooltip>
      <nav className="window-titlebar__menus">
        <TopMenu
          id="view"
          label={t("titlebar.view")}
          openMenu={openMenu}
          setOpenMenu={setOpenMenu}
        >
          <DropdownMenuItem onSelect={act("view.reload")}>
            {t("titlebar.reload")}
            <DropdownMenuShortcut>Ctrl+R</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={act("view.devtools")}>
            {t("titlebar.devtools")}
            <DropdownMenuShortcut>Ctrl+Shift+I</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={act("view.zoomIn")}>
            {t("titlebar.zoomIn")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={act("view.zoomOut")}>
            {t("titlebar.zoomOut")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={act("view.zoomReset")}>
            {t("titlebar.zoomReset")}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={act("view.fullscreen")}>
            {t("titlebar.fullscreen")}
            <DropdownMenuShortcut>F11</DropdownMenuShortcut>
          </DropdownMenuItem>
        </TopMenu>

        <TopMenu
          id="help"
          label={t("titlebar.help")}
          openMenu={openMenu}
          setOpenMenu={setOpenMenu}
        >
          <DropdownMenuItem onSelect={act("help.shortcuts")}>
            {t("settings.keyboardShortcuts")}
            <DropdownMenuShortcut>Ctrl+/</DropdownMenuShortcut>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={act("help.about")}>
            {t("titlebar.about")}
          </DropdownMenuItem>
        </TopMenu>
      </nav>
      <div className="window-titlebar__spacer" />
      <WindowControls isMaximized={isMaximized} t={t} />
    </header>
  );
}
