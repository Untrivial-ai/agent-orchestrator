export const MAX_TESTIMONIAL_WORDS = 350;

export function countWords(value: string) {
	return value.trim() ? value.trim().split(/\s+/u).length : 0;
}

export function keepWithinWordLimit(
	nextValue: string,
	currentValue: string,
	maxWords = MAX_TESTIMONIAL_WORDS,
) {
	return countWords(nextValue) <= maxWords ? nextValue : currentValue;
}

export function isLinkedInProfileUrl(value: string) {
	try {
		const url = new URL(value);
		const hostname = url.hostname.toLowerCase().replace(/^www\./, "");
		return (
			url.protocol === "https:" &&
			hostname === "linkedin.com" &&
			/^\/in\/[^/]+\/?$/u.test(url.pathname)
		);
	} catch {
		return false;
	}
}

export function isTweetUrl(value: string) {
	try {
		const url = new URL(value);
		const hostname = url.hostname.toLowerCase().replace(/^(?:www\.|mobile\.)/, "");
		return (
			url.protocol === "https:" &&
			(hostname === "x.com" || hostname === "twitter.com") &&
			/^\/[^/]+\/status\/\d+\/?$/u.test(url.pathname)
		);
	} catch {
		return false;
	}
}
