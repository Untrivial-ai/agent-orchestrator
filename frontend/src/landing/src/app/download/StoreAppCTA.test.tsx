import { load } from "cheerio";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const platform = { platform: "unknown", mobileOS: null as string | null };

vi.mock("../../lib/analytics", () => ({ track: vi.fn() }));
vi.mock("../hooks/useOS", () => ({
  Platform: { Mobile: "mobile", Unknown: "unknown" },
  usePlatform: () => platform,
}));

import { AndroidAppCTA } from "./AndroidAppCTA";
import { MobileAppCTA } from "./MobileAppCTA";

beforeEach(() => {
  platform.platform = "unknown";
  platform.mobileOS = null;
});

describe("store CTAs", () => {
  it("links an iOS visitor straight to the App Store listing", () => {
    platform.platform = "mobile";
    platform.mobileOS = "ios";

    const $ = load(renderToStaticMarkup(<MobileAppCTA />));
    const badge = $("a");

    expect(badge.attr("href")).toContain("apps.apple.com");
    // A pinned storefront 404s for everyone outside it.
    expect(badge.attr("href")).not.toContain("/us/");
    expect(badge.attr("aria-label")).toBe("Download on the App Store");
    expect($("img").attr("alt")).toBe("Download on the App Store");
    expect($("button")).toHaveLength(0);
  });

  it("links an Android visitor straight to the Play Store listing", () => {
    platform.platform = "mobile";
    platform.mobileOS = "android";

    const $ = load(renderToStaticMarkup(<AndroidAppCTA />));
    const badge = $("a");

    expect(badge.attr("href")).toContain("play.google.com/store/apps");
    expect(badge.attr("aria-label")).toBe("Get it on Google Play");
    expect($("img").attr("alt")).toBe("Get it on Google Play");
    expect($("button")).toHaveLength(0);
  });

  it("hands a desktop visitor the QR trigger instead of a dead link", () => {
    const $ = load(
      renderToStaticMarkup(
        <>
          <MobileAppCTA />
          <AndroidAppCTA />
        </>,
      ),
    );

    expect($("button")).toHaveLength(2);
    expect($("a")).toHaveLength(0);
    // Closed until asked for.
    expect($("[role=dialog]")).toHaveLength(0);
  });

  it("keeps an Android visitor off the iOS badge and vice versa", () => {
    platform.platform = "mobile";
    platform.mobileOS = "android";

    const $ = load(renderToStaticMarkup(<MobileAppCTA />));

    expect($("a")).toHaveLength(0);
    expect($("button")).toHaveLength(1);
  });
});
