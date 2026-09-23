// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";

describe("checkDesktopVersionFloor", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
  });

  async function importModule(options: {
    isPackaged?: boolean;
    version?: string;
    floor?: { min?: string; latest?: string; downloadUrl?: string };
    fetchError?: boolean;
    fetchStatus?: number;
  } = {}) {
    const dialog = { showMessageBox: vi.fn(() => Promise.resolve({ response: 1 })) };
    const shellMock = { openExternal: vi.fn(() => Promise.resolve()) };
    const quit = vi.fn();
    vi.doMock("electron", () => ({
      app: {
        isPackaged: options.isPackaged ?? true,
        getVersion: () => options.version ?? "0.12.12",
        quit,
      },
      dialog,
      shell: shellMock,
    }));

    const fetchMock = vi.fn(async () => {
      if (options.fetchError) throw new Error("network error");
      return {
        ok: (options.fetchStatus ?? 200) === 200,
        status: options.fetchStatus ?? 200,
        json: async () => options.floor ?? { min: "", latest: "" },
      } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);

    const mod = await import("./desktop-version-floor");
    return { mod, dialog, shellMock, quit, fetchMock };
  }

  it("does nothing when app is not packaged", async () => {
    const { mod, fetchMock } = await importModule({ isPackaged: false });
    await mod.checkDesktopVersionFloor();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does nothing when floor is empty", async () => {
    const { mod, dialog } = await importModule({ floor: { min: "", latest: "" } });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });

  it("does nothing when fetch fails", async () => {
    const { mod, dialog } = await importModule({ fetchError: true });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });

  it("does nothing when fetch returns non-200", async () => {
    const { mod, dialog } = await importModule({ fetchStatus: 404 });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });

  it("does nothing when running version is at or above min", async () => {
    const { mod, dialog } = await importModule({
      version: "0.12.13",
      floor: { min: "0.12.13" },
    });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });

  it("shows a blocking dialog and quits when below min", async () => {
    const { mod, dialog, quit } = await importModule({
      version: "0.12.12",
      floor: { min: "0.12.13" },
    });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "warning",
        title: "Update Required",
      }),
    );
    expect(quit).toHaveBeenCalled();
  });

  it("opens the download URL when user clicks Download on required update", async () => {
    const { mod, dialog, shellMock, quit } = await importModule({
      version: "0.12.12",
      floor: { min: "0.12.13", downloadUrl: "https://github.com/Untrivial-ai/agent-orchestrator/releases/tag/v0.12.13" },
    });
    dialog.showMessageBox.mockResolvedValue({ response: 0 });
    await mod.checkDesktopVersionFloor();
    expect(shellMock.openExternal).toHaveBeenCalledWith("https://github.com/Untrivial-ai/agent-orchestrator/releases/tag/v0.12.13");
    expect(quit).toHaveBeenCalled();
  });

  it("shows a dismissible dialog when below latest but at or above min", async () => {
    const { mod, dialog, quit } = await importModule({
      version: "0.12.12",
      floor: { min: "0.12.11", latest: "0.12.13" },
    });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "info",
        title: "Update Available",
      }),
    );
    expect(quit).not.toHaveBeenCalled();
  });

  it("does not show the latest nudge when at or above latest", async () => {
    const { mod, dialog } = await importModule({
      version: "0.12.14",
      floor: { latest: "0.12.13" },
    });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });

  it("ignores malformed version strings in the floor", async () => {
    const { mod, dialog } = await importModule({
      version: "0.12.12",
      floor: { min: "not-a-version", latest: "also bad" },
    });
    await mod.checkDesktopVersionFloor();
    expect(dialog.showMessageBox).not.toHaveBeenCalled();
  });
});
