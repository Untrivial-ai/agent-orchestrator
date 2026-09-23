/**
 * Builds the provider's public avatar endpoint when an observation has only a
 * user id. Authoritative avatar URLs from provider APIs should be preferred.
 */
export function scmUserAvatarUrl(
	provider: string,
	pullRequestUrl: string | undefined,
	userId: string,
): string | undefined {
	const normalizedUserId = userId.replace(/^@/, "").trim();
	if (!normalizedUserId || !pullRequestUrl) return undefined;

	let origin: string;
	try {
		origin = new URL(pullRequestUrl).origin;
	} catch {
		return undefined;
	}

	const encodedUserId = encodeURIComponent(normalizedUserId);
	if (provider === "github") {
		if (origin === "https://github.com") {
			return `https://avatars.githubusercontent.com/${encodedUserId}?size=64`;
		}
		return `${origin}/${encodedUserId}.png?size=64`;
	}
	if (provider === "gitlab") return `${origin}/-/avatar?username=${encodedUserId}`;
	return undefined;
}
