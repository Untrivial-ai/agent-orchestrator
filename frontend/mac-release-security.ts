type ReleaseEnvironment = Record<string, string | undefined>;

interface MacPackagerSecurity {
	osxSign?: {
		identity?: string;
		hardenedRuntime: true;
		entitlements: string;
		entitlementsInherit: string;
	};
	osxNotarize?:
		| { keychainProfile: string }
		| { appleApiKey: string; appleApiKeyId: string; appleApiIssuer: string };
}

const APP_STORE_CONNECT_KEYS = [
	"APPLE_API_KEY",
	"APPLE_API_KEY_ID",
	"APPLE_API_ISSUER",
] as const;

function configured(value: string | undefined): boolean {
	return Boolean(value?.trim());
}

function credentialState(env: ReleaseEnvironment) {
	const apiValues = APP_STORE_CONNECT_KEYS.map((key) => configured(env[key]));
	const anyApiCredential = apiValues.some(Boolean);
	const completeApiCredentials = apiValues.every(Boolean);
	const hasSigning =
		configured(env.APPLE_SIGNING_IDENTITY) || configured(env.CSC_LINK);
	const hasNotarization =
		configured(env.AO_NOTARY_PROFILE) || completeApiCredentials;
	return {
		anyApiCredential,
		completeApiCredentials,
		hasSigning,
		hasNotarization,
	};
}

export function isForgePublishCommand(argv: readonly string[]): boolean {
	return argv.some((argument) => argument === "publish");
}

export function assertMacReleaseCredentials(
	env: ReleaseEnvironment,
	platform: NodeJS.Platform,
	isPublish: boolean,
): void {
	if (platform !== "darwin") return;
	const {
		anyApiCredential,
		completeApiCredentials,
		hasSigning,
		hasNotarization,
	} = credentialState(env);

	if (anyApiCredential && !completeApiCredentials) {
		throw new Error(
			"APPLE_API_KEY, APPLE_API_KEY_ID, and APPLE_API_ISSUER must be set together",
		);
	}
	if (hasSigning !== hasNotarization) {
		throw new Error(
			"macOS packaging requires signing and notarization credentials together",
		);
	}
	if (isPublish && !hasSigning) {
		throw new Error(
			"macOS publish requires both signing and notarization credentials",
		);
	}
}

export function macPackagerSecurity(
	env: ReleaseEnvironment,
): MacPackagerSecurity {
	const { completeApiCredentials, hasSigning } = credentialState(env);
	return {
		osxSign: hasSigning
			? {
					...(configured(env.APPLE_SIGNING_IDENTITY)
						? { identity: env.APPLE_SIGNING_IDENTITY }
						: {}),
					hardenedRuntime: true,
					entitlements: "assets/entitlements.mac.plist",
					entitlementsInherit: "assets/entitlements.mac.inherit.plist",
				}
			: undefined,
		osxNotarize: configured(env.AO_NOTARY_PROFILE)
			? { keychainProfile: env.AO_NOTARY_PROFILE as string }
			: completeApiCredentials
				? {
						appleApiKey: env.APPLE_API_KEY as string,
						appleApiKeyId: env.APPLE_API_KEY_ID as string,
						appleApiIssuer: env.APPLE_API_ISSUER as string,
					}
				: undefined,
	};
}
