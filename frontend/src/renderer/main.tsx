import "./lib/apply-initial-theme";
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import "@xterm/xterm/css/xterm.css";
import "./styles.css";
import { queryClient } from "./lib/query-client";
import { mergeUnreadNotification, unreadNotificationsQueryKey } from "./lib/notifications";
import { createAppRouter } from "./router";
import { TelemetryBoundary } from "./components/TelemetryBoundary";
import { CloudOnboardingGate } from "./components/CloudOnboardingGate";
import { applyRendererTelemetryPolicy, clearRendererTelemetryQueues, initTelemetry } from "./lib/telemetry";
import { aoBridge } from "./lib/bridge";
import { startDaemonFailureTelemetry } from "./lib/daemon-telemetry";
import { startUpdateTelemetry } from "./lib/update-telemetry";
import { appI18n } from "./i18n";
import { useLocaleStore } from "./stores/locale-store";
import { useSoundNotificationsStore } from "./stores/sound-notifications-store";
import { useTelemetryPolicyStore } from "./stores/telemetry-policy-store";

const router = createAppRouter(queryClient);

// Main owns consent and only acknowledges opt-out after every live AO shell
// confirms that its in-memory renderer queues were actually purged.
aoBridge.telemetry.onClearQueues(clearRendererTelemetryQueues);
aoBridge.telemetry.onPolicy((view) => applyRendererTelemetryPolicy(view.eventsEnabled && view.acknowledged && view.state === "applied"));

if (import.meta.env.DEV) {
	const w = window as never as Record<string, unknown>;
	w.__qc = queryClient;
	// __testNotif("needs_input") — simulates a real notification:
	//   bell count + dock badge update immediately; dock bounces after 3s
	//   (gives you time to click away from AO so the bounce is visible)
	const testNotifTitles: Record<string, string> = {
		needs_input: "Agent needs your input",
		ready_to_merge: "Ready to merge",
		pr_merged: "PR merged",
		pr_closed_unmerged: "PR closed",
	};
	w.__testNotif = async (
		type: "needs_input" | "ready_to_merge" | "pr_merged" | "pr_closed_unmerged" = "needs_input",
	) => {
		const key = unreadNotificationsQueryKey;
		const id = `test-${Date.now()}`;
		// Freeze the query so window-focus refetch doesn't wipe test data
		queryClient.setQueryDefaults(key, { staleTime: 60_000 });
		await queryClient.cancelQueries({ queryKey: key });
		mergeUnreadNotification(queryClient, {
			id,
			type,
			title: testNotifTitles[type] ?? "Notification",
			body: "Test notification",
			createdAt: new Date().toISOString(),
			sessionId: "",
			projectId: "",
			prUrl: "",
			target: { kind: "session", sessionId: "" },
			status: "unread",
		});
		console.log("[testNotif] bell updated - click away from AO now, bounce fires in 3s");
		setTimeout(() => {
			void window.ao?.notifications.devBounce();
			// Restore normal stale time after bounce
			queryClient.setQueryDefaults(key, { staleTime: 0 });
		}, 3000);
	};
}

void initTelemetry();
startDaemonFailureTelemetry();
startUpdateTelemetry();

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}
}

async function renderApp(): Promise<void> {
	void useTelemetryPolicyStore.getState().load();
	// The persisted locale is cosmetic; do not leave a newly opened native
	// window blank while its IPC read completes. The router's pending screen
	// renders immediately, then i18n updates if the user chose another locale.
	void useLocaleStore.getState().load();
	// The sound-notifications toggle only needs to be right by the time
	// Settings renders, so it loads in the background rather than blocking mount.
	void useSoundNotificationsStore.getState().load();
	createRoot(document.getElementById("root") as HTMLElement).render(
		<React.StrictMode>
			<I18nextProvider i18n={appI18n}>
				<TelemetryBoundary>
					<QueryClientProvider client={queryClient}>
						<RouterProvider router={router} />
						<CloudOnboardingGate />
					</QueryClientProvider>
				</TelemetryBoundary>
			</I18nextProvider>
		</React.StrictMode>,
	);
}

void renderApp();
