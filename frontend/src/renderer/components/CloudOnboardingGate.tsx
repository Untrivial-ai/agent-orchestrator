import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useCloudGate } from "../hooks/useCloudGate";
import { useCloudSession } from "../lib/cloud-session";
import { cloudOrgQueryKey, useCloudOrg } from "../hooks/useCloudOrg";
import { cloudProjectsQueryKey, cloudSessionsQueryKey } from "../hooks/useWorkspaceQuery";
import { hasValidAgentConnection, useProviderConnections } from "../hooks/useProviderConnections";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { CloudCredentialDialog } from "./CloudCredentialDialog";
import { CloudLocalSignInDialog } from "./CloudLocalSignInDialog";

// Mounted once at the app root. When the cloud offering is on and the developer
// has signed in but their org has no validated coding-agent connection yet, it
// prompts once for a setup token (the in-app replacement for the dev script).
// It also renders the single credential dialog instance the manual entry point
// (sidebar account row) drives through the shared store.
export function CloudOnboardingGate() {
	const { cloudEnabled } = useCloudGate();
	const { status } = useCloudSession();
	const { org } = useCloudOrg();
	const connections = useProviderConnections(org?.id);
	const openDialog = useCredentialDialogStore((s) => s.openDialog);
	const queryClient = useQueryClient();
	// Prompt at most once per signed-in session so a developer who dismisses
	// without connecting is not re-nagged every render.
	const autoPromptedRef = useRef(false);

	const signedIn = cloudEnabled && status === "authenticated";

	// Sign-out must also drop the cached cloud data: the queries merely become
	// disabled, and their stale results would otherwise keep cloud projects on
	// the board (or flash another account's data on the next sign-in).
	useEffect(() => {
		if (status !== "unauthenticated") return;
		queryClient.removeQueries({ queryKey: cloudProjectsQueryKey });
		queryClient.removeQueries({ queryKey: cloudSessionsQueryKey });
		queryClient.removeQueries({ queryKey: cloudOrgQueryKey });
		queryClient.removeQueries({ queryKey: ["cloud-provider-connections"] });
	}, [status, queryClient]);

	useEffect(() => {
		if (!signedIn) {
			autoPromptedRef.current = false;
			return;
		}
		if (org === undefined || !connections.isSuccess || autoPromptedRef.current) return;
		if (!hasValidAgentConnection(connections.data)) {
			autoPromptedRef.current = true;
			openDialog();
		}
	}, [signedIn, org, connections.isSuccess, connections.data, openDialog]);

	if (!cloudEnabled) return null;
	return (
		<>
			<CloudCredentialDialog />
			<CloudLocalSignInDialog />
		</>
	);
}
