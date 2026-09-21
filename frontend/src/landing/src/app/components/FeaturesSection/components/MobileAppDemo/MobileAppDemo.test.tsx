import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const motionPreference = vi.hoisted(() => ({ reduced: false }));

vi.mock("motion/react", async (importOriginal) => ({
  ...await importOriginal<typeof import("motion/react")>(),
  useReducedMotion: () => motionPreference.reduced,
}));

import { MobileAppDemo } from "./MobileAppDemo";

describe("MobileAppDemo", () => {
  let container: HTMLDivElement;
  let root: ReturnType<typeof createRoot>;

  beforeEach(() => {
    motionPreference.reduced = false;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it("opens the revamped navigation and switches between mobile destinations", async () => {
    await act(async () => root.render(<MobileAppDemo />));

    expect(heading("Workers")).not.toBeNull();

    await click(button("Open navigation"));
    expect(container.querySelector('nav[aria-label="Mobile app"]')).not.toBeNull();

    await click(button("Projects"));
    await settle();

    expect(heading("Projects")).not.toBeNull();
    expect(container.querySelector('nav[aria-label="Mobile app"]')).toBeNull();
  });

  it("closes the navigation with Escape and returns focus to its trigger", async () => {
    await act(async () => root.render(<MobileAppDemo />));

    const trigger = button("Open navigation");
    await click(trigger);
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await settle();

    expect(container.querySelector('nav[aria-label="Mobile app"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("treats the open navigation as a modal and contains keyboard focus", async () => {
    await act(async () => root.render(<MobileAppDemo />));

    const trigger = button("Open navigation");
    await click(trigger);

    const dialog = container.querySelector('[role="dialog"][aria-modal="true"]');
    if (!(dialog instanceof HTMLElement)) throw new Error("Navigation dialog not found");
    const navigation = dialog.querySelector('nav[aria-label="Mobile app"]');
    if (!(navigation instanceof HTMLElement)) throw new Error("Navigation not found");
    const close = navigation.querySelector('button[aria-label="Close navigation"]');
    const settings = [...navigation.querySelectorAll("button")].find(
      (element) => element.textContent?.trim() === "Settings",
    );
    if (!(close instanceof HTMLButtonElement) || !(settings instanceof HTMLButtonElement)) {
      throw new Error("Navigation boundary controls not found");
    }

    expect(document.activeElement).toBe(close);
    expect(trigger.closest('[aria-hidden="true"]')).not.toBeNull();

    settings.focus();
    await keydown("Tab");
    expect(document.activeElement).toBe(close);

    close.focus();
    await keydown("Tab", { shiftKey: true });
    expect(document.activeElement).toBe(settings);
  });

  it("keeps spawn compact and gives every project a text-only orchestrator action", async () => {
    await act(async () => root.render(<MobileAppDemo />));

    expect(button("New worker").textContent?.trim()).toBe("");

    await click(button("Open navigation"));
    await click(button("Projects"));
    await settle();

    const orchestratorActions = [...container.querySelectorAll("button")].filter(
      (element) => element.textContent?.trim() === "Orchestrator",
    );
    expect(orchestratorActions).toHaveLength(3);
    expect(orchestratorActions.every((element) => element.querySelector("svg") === null)).toBe(true);
  });

  it("splits worker controls into a left cluster and a standalone spawn action", async () => {
    await act(async () => root.render(<MobileAppDemo />));

    const actions = container.querySelector('[aria-label="Worker actions"]');
    if (!(actions instanceof HTMLElement)) throw new Error("Worker actions not found");

    expect(actions.children).toHaveLength(2);
    expect(button("Worker filters").parentElement).toBe(button("Search workers").parentElement);
    expect(button("New worker").parentElement).toBe(actions);
    expect(actions.querySelector('[role="separator"]')).toBeNull();
  });

  it("reveals search without motion when reduced motion is preferred", async () => {
    motionPreference.reduced = true;
    await act(async () => root.render(<MobileAppDemo />));

    await click(button("Search workers"));

    const searchPanel = button("Close search").parentElement;
    if (!(searchPanel instanceof HTMLElement)) throw new Error("Search panel not found");
    expect(searchPanel.style.opacity).not.toBe("0");
    expect(searchPanel.style.transform).not.toContain("translateY");
  });

  function button(name: string): HTMLButtonElement {
    const match = [...container.querySelectorAll("button")].find(
      (element) => element.getAttribute("aria-label") === name || element.textContent?.trim() === name,
    );
    if (!(match instanceof HTMLButtonElement)) throw new Error(`Button not found: ${name}`);
    return match;
  }

  function heading(name: string): HTMLHeadingElement | null {
    return [...container.querySelectorAll("h1,h2,h3,h4,h5,h6")].find(
      (element) => element.textContent?.trim() === name,
    ) as HTMLHeadingElement | undefined ?? null;
  }

  async function click(element: HTMLButtonElement) {
    await act(async () => element.click());
  }

  async function settle() {
    await act(async () => new Promise((resolve) => setTimeout(resolve, 400)));
  }

  async function keydown(key: string, init: KeyboardEventInit = {}) {
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", {
      key,
      bubbles: true,
      cancelable: true,
      ...init,
    })));
  }
});
