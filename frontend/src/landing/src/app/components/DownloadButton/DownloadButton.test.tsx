import { load } from "cheerio";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Platform } from "../../hooks/useOS";
import { DownloadButton, getDownloadIconKind } from "./DownloadButton";

describe("DownloadButton", () => {
  it("renders stable copy with CSS-selected mobile and desktop icons", () => {
    const $ = load(renderToStaticMarkup(<DownloadButton />));
    const link = $("a");
    const icons = link.find("[data-download-icon]");

    expect(link.attr("href")).toBe("/download");
    expect(link.find("[data-download-label]").text()).toBe("Download");
    expect(icons).toHaveLength(2);
    expect(icons.eq(0).hasClass("md:hidden")).toBe(true);
    expect(icons.eq(1).hasClass("hidden")).toBe(true);
    expect(icons.eq(1).hasClass("md:inline-flex")).toBe(true);
  });

  it("keeps the original icon matched to the detected desktop platform", () => {
    expect(getDownloadIconKind(Platform.Mobile)).toBe("mobile");
    expect(getDownloadIconKind(Platform.Windows)).toBe("windows");
    expect(getDownloadIconKind(Platform.Linux)).toBe("linux");
    expect(getDownloadIconKind(Platform.MacAppleSilicon)).toBe("apple");
    expect(getDownloadIconKind(Platform.MacIntel)).toBe("apple");
    expect(getDownloadIconKind(Platform.Unknown)).toBe("apple");
  });
});
