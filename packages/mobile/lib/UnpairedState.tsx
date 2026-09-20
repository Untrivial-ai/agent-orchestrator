import { useRouter } from "expo-router";

import { Button, EmptyState } from "./ui";

/**
 * What every screen shows before a desktop has been paired.
 *
 * The three tabs each had their own answer to this: Workers explained what was
 * missing and offered the scanner, while Projects and PRs said "No server /
 * Connect to AO in Settings" with no way to act on it — and sent the user to
 * hunt through Settings for a field rather than to the scanner that fixes it.
 * PRs did not even render a header, so the tab lost its title at the one moment
 * a new user most needs to know where they are.
 *
 * This is the Workers copy, which was the good one, made shared. Deliberately
 * not a restatement of the welcome screen: someone reaching this has already
 * read that and chosen to move past it.
 */
export function UnpairedState() {
	const router = useRouter();
	return (
		<EmptyState
			icon="server"
			title="No desktop paired"
			message="Scan the pairing code from AO → Settings → Connect Mobile to drive your agents from here."
			action={<Button title="Scan pairing code" icon="maximize" onPress={() => router.push("/pair")} />}
		/>
	);
}
