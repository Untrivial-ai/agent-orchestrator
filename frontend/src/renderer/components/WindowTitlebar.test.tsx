import { readFileSync } from "node:fs";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";

const { navigateMock } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
  useRouter: () => ({
    history: {
      back: vi.fn(),
      forward: vi.fn(),
      location: { state: { __TSR_index: 0 } },
      subscribe: () => () => undefined,
    },
  }),
  useCanGoBack: () => false,
}));

vi.mock("./TitlebarNav", () => ({
  useCanGoForward: () => false,
}));

describe("WindowTitlebar", () => {
  let actionMock: (action: string) => Promise<void>;
  const platformDescriptor = Object.getOwnPropertyDescriptor(
    navigator,
    "platform",
  );

  async function loadWindowTitlebar(platform = "Win32") {
    vi.resetModules();
    Object.defineProperty(navigator, "platform", {
      configurable: true,
      value: platform,
    });
    return import("./WindowTitlebar");
  }

  beforeEach(() => {
    navigateMock.mockReset();
    actionMock = vi.fn(async (_action: string) => undefined);
    window.ao!.menu.action = actionMock;
    document.documentElement.removeAttribute("style");
  });

  afterEach(() => {
    if (platformDescriptor) {
      Object.defineProperty(navigator, "platform", platformDescriptor);
    } else {
      Reflect.deleteProperty(navigator, "platform");
    }
  });

  it("renders custom Windows controls and dispatches window actions", async () => {
    const { WindowTitlebar } = await loadWindowTitlebar();

    render(
      <TooltipProvider>
        <WindowTitlebar />
      </TooltipProvider>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Minimize" }));
    await userEvent.click(
      screen.getByRole("button", { name: "Maximize / Restore" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Close" }));

    expect(actionMock).toHaveBeenNthCalledWith(1, "window.minimize");
    expect(actionMock).toHaveBeenNthCalledWith(2, "window.maximize");
    expect(actionMock).toHaveBeenNthCalledWith(3, "window.close");
  });

  it("shows only View and Help in the top menu", async () => {
    const { WindowTitlebar } = await loadWindowTitlebar();

    render(
      <TooltipProvider>
        <WindowTitlebar />
      </TooltipProvider>,
    );

    expect(screen.getByRole("button", { name: "View" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Help" })).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "File" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Window" }),
    ).not.toBeInTheDocument();
  });

  it("renders the back and forward navigation buttons", async () => {
    const { WindowTitlebar } = await loadWindowTitlebar();

    render(
      <TooltipProvider>
        <WindowTitlebar />
      </TooltipProvider>,
    );

    expect(screen.getByRole("button", { name: "Go back" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Go forward" }),
    ).toBeInTheDocument();
  });

  it("switches the maximize control to a restore icon when the window is maximized", async () => {
    let reportMaximized: ((maximized: boolean) => void) | undefined;
    window.ao!.window.isMaximized = vi.fn().mockResolvedValue(false);
    window.ao!.window.onMaximized = (listener) => {
      reportMaximized = listener;
      return () => undefined;
    };
    const { WindowTitlebar } = await loadWindowTitlebar();

    render(
      <TooltipProvider>
        <WindowTitlebar />
      </TooltipProvider>,
    );
    const maximizeButton = screen.getByRole("button", {
      name: "Maximize / Restore",
    });
    await waitFor(() =>
      expect(maximizeButton.querySelector(".lucide-square")).not.toBeNull(),
    );

    reportMaximized?.(true);
    await waitFor(() =>
      expect(maximizeButton.querySelector(".lucide-copy")).not.toBeNull(),
    );
  });

  it.each(["Linux x86_64", "MacIntel"])(
    "does not render Windows controls on %s",
    async (platform) => {
      const { WindowTitlebar } = await loadWindowTitlebar(platform);

      const { container } = render(
        <TooltipProvider>
          <WindowTitlebar />
        </TooltipProvider>,
      );

      expect(container).toBeEmptyDOMElement();
      expect(actionMock).not.toHaveBeenCalled();
    },
  );

  it("keeps Windows spacing and overlays scoped to Windows", () => {
    const css = readFileSync("src/renderer/styles.css", "utf8");
    const tokens = readFileSync("src/styles/tokens.css", "utf8");

    expect(css).toContain(".platform-windows .window-titlebar__controls");
    expect(css).toMatch(
      /html\[data-native-browser-composition="true"\]\[data-ao-platform="win32"\] \.browser-popout-overlay,\s*html\[data-native-browser-composition="true"\]\[data-ao-platform="win32"\] \.files-popout-overlay\s*{\s*top: var\(--size-window-titlebar\);/s,
    );
    expect(css).toMatch(
      /body:has\(#root \.platform-windows\) > \.browser-popout-overlay,\s*body:has\(#root \.platform-windows\) > \.files-popout-overlay\s*{\s*top: var\(--size-window-titlebar\);/s,
    );
    expect(css).toMatch(
      /\.platform-windows\s*{\s*--size-center-panel-inset: 8px;\s*--size-center-panel-inline-inset: 8px;\s*--size-center-panel-bottom-inset: 8px;/s,
    );
    expect(css).toMatch(
      /\.platform-linux \.browser-popout-overlay,\s*\.platform-linux \.files-popout-overlay\s*{\s*top: var\(--size-shell-topbar\);/s,
    );
    expect(tokens).toMatch(
      /--size-center-panel-inset: 24px;\s*--size-center-panel-inline-inset: 16px;\s*--size-center-panel-bottom-inset: 14px;/s,
    );
    expect(tokens).toContain("--size-titlebar-content-offset-linux");
    expect(css).toContain(".center-panel-shell--titlebar-clearance-linux .center-panel-titlebar");
  });
});
