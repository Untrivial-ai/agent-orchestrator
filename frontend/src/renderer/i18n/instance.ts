import { createInstance, type i18n } from "i18next";
import { initReactI18next } from "react-i18next";
import { APP_LOCALES, DEFAULT_LOCALE, type AppLocale } from "./locales";
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

export type TranslationCatalogs = Record<AppLocale, Record<string, string | string[]>>;

export const appCatalogs: TranslationCatalogs = {
	en: enMessages,
	"zh-CN": zhCNMessages,
	ja: jaMessages,
	ko: koMessages,
	es: esMessages,
	fr: frMessages,
	de: deMessages,
	"pt-BR": ptBRMessages,
};

/** Create an isolated, synchronously initialized instance for app startup and unit tests. */
export function createAppI18n(locale: AppLocale = DEFAULT_LOCALE, catalogs: TranslationCatalogs = appCatalogs): i18n {
	return initializeI18n(createInstance(), locale, catalogs);
}

function initializeI18n(instance: i18n, locale: AppLocale, catalogs: TranslationCatalogs): i18n {
	const resources = Object.fromEntries(
		APP_LOCALES.map((lng) => [lng, { translation: catalogs[lng] ?? {} }]),
	);
	void instance.init({
		lng: locale,
		fallbackLng: DEFAULT_LOCALE,
		supportedLngs: [...APP_LOCALES],
		load: "currentOnly",
		resources,
		defaultNS: "translation",
		keySeparator: false,
		nsSeparator: false,
		returnNull: false,
		initAsync: false,
		interpolation: { escapeValue: false },
	});
	return instance;
}

export const appI18n = initializeI18n(createInstance().use(initReactI18next), DEFAULT_LOCALE, appCatalogs);
