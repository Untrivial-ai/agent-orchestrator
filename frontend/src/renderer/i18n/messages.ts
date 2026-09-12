import en from "./en.json";
import zhCN from "./zh-CN.json";
import ja from "./ja.json";
import ko from "./ko.json";
import es from "./es.json";
import fr from "./fr.json";
import de from "./de.json";
import ptBR from "./pt-BR.json";
import type { AppLocale } from "./locales";

/** English is the source-of-truth catalog; keys are typed from it. */
export const enMessages = en;
export const zhCNMessages = zhCN;
export const jaMessages = ja;
export const koMessages = ko;
export const esMessages = es;
export const frMessages = fr;
export const deMessages = de;
export const ptBRMessages = ptBR;

export type MessageKey = {
	[K in keyof typeof enMessages]: typeof enMessages[K] extends string ? K : never;
}[keyof typeof enMessages];

type PluralCategory = "zero" | "one" | "two" | "few" | "many" | "other";
export type PluralMessageKey = MessageKey extends infer Key extends string
	? Key extends `${infer Base}_${PluralCategory}`
		? Base
		: never
	: never;

export type MessageCatalog = Record<MessageKey, string>;

const catalogs: Record<AppLocale, MessageCatalog> = {
	en: enMessages as unknown as MessageCatalog,
	"zh-CN": zhCNMessages as unknown as MessageCatalog,
	ja: jaMessages as unknown as MessageCatalog,
	ko: koMessages as unknown as MessageCatalog,
	es: esMessages as unknown as MessageCatalog,
	fr: frMessages as unknown as MessageCatalog,
	de: deMessages as unknown as MessageCatalog,
	"pt-BR": ptBRMessages as unknown as MessageCatalog,
};

export function catalogFor(locale: AppLocale): MessageCatalog {
	return catalogs[locale] ?? catalogs.en;
}
