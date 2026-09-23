import { appI18n } from "../i18n";

export function browserTabLabel(title: string, url: string): { title: string; subtitle: string } {
	const cleanTitle = title.trim();
	const hasPlaceholderTitle = cleanTitle === "about:blank";
	if (!url || url === "about:blank" || hasPlaceholderTitle) {
		return {
			title: hasPlaceholderTitle ? appI18n.t("browser.newTab") : cleanTitle || appI18n.t("browser.newTab"),
			subtitle: appI18n.t("browser.blankPage"),
		};
	}
	try {
		const parsed = new URL(url);
		const subtitle = parsed.protocol === "file:" ? parsed.pathname.split("/").filter(Boolean).at(-1) || url : parsed.host;
		return { title: cleanTitle || subtitle, subtitle };
	} catch {
		return { title: cleanTitle || url, subtitle: url };
	}
}
