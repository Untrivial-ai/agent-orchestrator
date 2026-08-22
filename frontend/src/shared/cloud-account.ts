export interface CloudAccount {
	authProvider: "google";
	user: {
		id: string;
		email: string;
		displayName: string;
	};
	organizations: Array<{
		id: string;
		slug: string;
		displayName: string;
		role: string;
	}>;
	storedAt: string;
}
