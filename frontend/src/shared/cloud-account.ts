// The identity provider that minted the active cloud session. "workos" is the
// production AuthKit path; "local" is the dev-only email/password path used to
// sign into a loopback Docker control plane running AO_CLOUD_LOCAL_AUTH (see
// main/cloud-auth-local.ts). Local sessions are gated to unpackaged/dev builds
// against a 127.0.0.1/localhost control plane and never reach production users.
export type CloudAuthProvider = "workos" | "local";

export interface CloudOrganization {
	id: string;
	slug: string;
	displayName: string;
	role: string;
}

export interface CloudAccount {
	authProvider: CloudAuthProvider;
	user: {
		id: string;
		email: string;
		displayName: string;
	};
	/** Organizations the user belongs to. Populated by the local provider; omitted by WorkOS. */
	organizations?: CloudOrganization[];
	storedAt: string;
}
