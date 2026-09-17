import { describe, expect, it } from "vitest";
import { APP_LOCALES, coerceLocale, DEFAULT_LOCALE } from "./locales";
import { createAppI18n, type TranslationCatalogs } from "./instance";
import {
	deMessages,
	enMessages,
	esMessages,
	frMessages,
	jaMessages,
	koMessages,
	ptBRMessages,
	zhCNMessages,
} from "./messages";

const allCatalogs = {
	en: enMessages,
	"zh-CN": zhCNMessages,
	ja: jaMessages,
	ko: koMessages,
	es: esMessages,
	fr: frMessages,
	de: deMessages,
	"pt-BR": ptBRMessages,
} as const;

function emptyCatalogs(): TranslationCatalogs {
	return Object.fromEntries(APP_LOCALES.map((locale) => [locale, {}])) as TranslationCatalogs;
}

describe("coerceLocale", () => {
	it("accepts all supported locales", () => {
		expect(coerceLocale("en")).toBe("en");
		expect(coerceLocale("zh-CN")).toBe("zh-CN");
		expect(coerceLocale("ja")).toBe("ja");
		expect(coerceLocale("ko")).toBe("ko");
		expect(coerceLocale("es")).toBe("es");
		expect(coerceLocale("fr")).toBe("fr");
		expect(coerceLocale("de")).toBe("de");
		expect(coerceLocale("pt-BR")).toBe("pt-BR");
	});

	it("defaults unknown values to en", () => {
		expect(coerceLocale(undefined)).toBe(DEFAULT_LOCALE);
		expect(coerceLocale(null)).toBe("en");
		expect(coerceLocale("pt")).toBe("en");
		expect(coerceLocale({ locale: "zh-CN" })).toBe("en");
	});
});

describe("app i18next instance", () => {
	it("uses English by default and Chinese when selected", () => {
		expect(createAppI18n().t("settings.general")).toBe("General");
		expect(createAppI18n("zh-CN").t("settings.general")).toBe("通用");
		expect(createAppI18n("zh-CN").t("settings.language.zhCN")).toBe("简体中文");
	});

	it("resolves native language labels for every supported locale", () => {
		const labels = {
			en: "settings.language.en",
			"zh-CN": "settings.language.zhCN",
			ja: "settings.language.ja",
			ko: "settings.language.ko",
			es: "settings.language.es",
			fr: "settings.language.fr",
			de: "settings.language.de",
			"pt-BR": "settings.language.ptBR",
		} as const;
		const expected = {
			en: "English",
			"zh-CN": "简体中文",
			ja: "日本語",
			ko: "한국어",
			es: "Español",
			fr: "Français",
			de: "Deutsch",
			"pt-BR": "Português (Brasil)",
		} as const;
		for (const locale of APP_LOCALES) {
			expect(createAppI18n(locale).t(labels[locale])).toBe(expected[locale]);
		}
	});

	it("falls back from Chinese to English and then to the key", () => {
		const catalogs = emptyCatalogs();
		catalogs.en = { "proof.onlyEn": "English only", "settings.general": "General" };
		catalogs["zh-CN"] = { "settings.general": "通用" };
		const instance = createAppI18n("zh-CN", catalogs);
		expect(instance.t("proof.onlyEn", { defaultValue: "proof.onlyEn" })).toBe("English only");
		expect(instance.t("settings.general")).toBe("通用");
		expect(instance.t("totally.missing", { defaultValue: "totally.missing" })).toBe("totally.missing");
	});

	it("uses standard interpolation and locale-aware plural forms", () => {
		const catalogs = emptyCatalogs();
		catalogs.en = {
			"proof.hello": "Hello, {{name}}!",
			"proof.item_one": "{{count}} item",
			"proof.item_other": "{{count}} items",
		};
		catalogs["zh-CN"] = {
			"proof.hello": "你好，{{name}}！",
			"proof.item_other": "{{count}} 项",
		};
		const english = createAppI18n("en", catalogs);
		const chinese = createAppI18n("zh-CN", catalogs);
		expect(english.t("proof.hello", { name: "AO", defaultValue: "proof.hello" })).toBe("Hello, AO!");
		expect(chinese.t("proof.hello", { name: "AO", defaultValue: "proof.hello" })).toBe("你好，AO！");
		expect(english.t("proof.item", { count: 1, defaultValue: "proof.item" })).toBe("1 item");
		expect(english.t("proof.item", { count: 2, defaultValue: "proof.item" })).toBe("2 items");
		expect(chinese.t("proof.item", { count: 2, defaultValue: "proof.item" })).toBe("2 项");
	});

	it.each([
		["es", ["1 abierta", "1 borrador", "1 cerrada"], ["2 abiertas", "2 borradores", "2 cerradas"]],
		["fr", ["1 ouverte", "1 brouillon", "1 fermée"], ["2 ouvertes", "2 brouillons", "2 fermées"]],
		["pt-BR", ["1 aberta", "1 rascunho", "1 fechada"], ["2 abertas", "2 rascunhos", "2 fechadas"]],
	] as const)("renders singular and plural PR progress suffixes in %s", (locale, singular, plural) => {
		const instance = createAppI18n(locale);
		const keys = ["pr.progress.open", "pr.progress.draft", "pr.progress.closed"] as const;
		expect(keys.map((key) => instance.t(key, { count: 1 }))).toEqual(singular);
		expect(keys.map((key) => instance.t(key, { count: 2 }))).toEqual(plural);
	});

	it("provides Chinese copy for the remaining audited shell surfaces", () => {
		const chinese = createAppI18n("zh-CN");
		expect(chinese.t("terminal.loadingOutput")).toBe("正在加载最新输出…");
		expect(chinese.t("terminal.startingSession")).toBe("正在启动会话");
		expect(chinese.t("inspector.open")).toBe("打开");
		expect(chinese.t("inspector.openTerminal")).toBe("打开终端");
		expect(chinese.t("createProject.gitSetupNotice")).toBe(
			"如果此文件夹需要设置 Git，AO 会先初始化仓库并创建首次提交，然后再启动。",
		);
		expect(chinese.t("settings.updates.currentVersion", { version: "v1.2.3" })).toBe("当前版本 - v1.2.3");
		expect(chinese.t("settings.updates.updateTo", { version: "v1.2.3" })).toBe("更新至 v1.2.3");
		expect(chinese.t("settings.updates.updateToLatest")).toBe("更新至最新版本");
		expect(chinese.t("settings.updates.restartInstall")).toBe("重启并安装");
		expect(chinese.t("settings.updates.noFeatureReleases")).toBe("暂无可用的功能版本。");
		expect(chinese.t("settings.language.saveFailed")).toBe("无法保存语言偏好设置。");
	});

	it("provides complete Chinese copy for residual actions and pluralized review comments", () => {
		const chinese = createAppI18n("zh-CN");
		expect(chinese.t("shortcut.customize")).toBe("自定义");
		expect(chinese.t("notify.earlierLoadFailed")).toBe("无法加载更早的通知。");
		expect(chinese.t("notify.loadingEarlier")).toBe("正在加载更早的通知…");
		expect(chinese.t("notify.retry")).toBe("重试");
		expect(chinese.t("report.cancel")).toBe("取消");
		expect(chinese.t("shell.openProjectDashboard", { name: "AO" })).toBe("打开 AO 看板");
		expect(chinese.t("shell.renameSession", { title: "任务" })).toBe("重命名任务");
		expect(chinese.t("shell.versionReady", { version: "9.9.9" })).toBe("v9.9.9 已就绪");
		expect(chinese.t("pr.unresolvedComments", { count: 2, name: "小明" })).toBe("小明有 2 条未解决的评论");
	});

	it("keeps unresolved placeholders visible", () => {
		const catalogs = emptyCatalogs();
		catalogs.en = { "proof.x": "keep {{missing}}" };
		expect(createAppI18n("en", catalogs).t("proof.x", { defaultValue: "proof.x" })).toBe("keep {{missing}}");
	});

	it("keeps locale catalogs covering every English key with non-empty values", () => {
		const enKeys = Object.keys(enMessages).sort();
		for (const locale of APP_LOCALES) {
			const catalog = allCatalogs[locale];
			for (const key of enKeys) {
				expect(key in catalog, `${locale} is missing ${key}`).toBe(true);
				expect(String(catalog[key as keyof typeof catalog] ?? "").length, `${locale}.${key} empty`).toBeGreaterThan(
					0,
				);
			}
		}
	});

	it("keeps interpolation variables aligned between locales", () => {
		const variables = (message: string | string[]) => {
			const msgs = Array.isArray(message) ? message : [message];
			return msgs.flatMap((msg) => [...msg.matchAll(/{{\s*([\w.-]+)\s*}}/g)].map((match) => match[1])).sort();
		};
		for (const locale of APP_LOCALES) {
			if (locale === "en") continue;
			const catalog = allCatalogs[locale] as Record<keyof typeof enMessages, string | string[]>;
			for (const key of Object.keys(enMessages) as (keyof typeof enMessages)[]) {
				expect(variables(catalog[key]), `${locale} placeholder mismatch for ${key}`).toEqual(
					variables(enMessages[key]),
				);
			}
		}
	});

	it("provides every CLDR plural form required by each supported locale", () => {
		const pluralSuffix = /_(zero|one|two|few|many|other)$/;
		const pluralBases = new Set(
			Object.keys(enMessages)
				.filter((key) => pluralSuffix.test(key))
				.map((key) => key.replace(pluralSuffix, "")),
		);
		for (const locale of APP_LOCALES) {
			const requiredCategories = new Intl.PluralRules(locale).resolvedOptions().pluralCategories;
			for (const base of pluralBases) {
				for (const category of requiredCategories) {
					expect(
						`${base}_${category}` in allCatalogs[locale],
						`${locale} is missing ${base}_${category}`,
					).toBe(true);
				}
			}
		}
	});
});
