/**
 * Daemon-owned user preferences.
 *
 * Read from and written to the daemon rather than held in the renderer, because
 * `ao spawn`, mobile, and headless spawns resolve the same value. A preference
 * kept in one client would look correct in Settings and disagree with the others.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { SessionMode } from "../types/workspace";

export const settingsQueryKey = ["settings"] as const;

export interface Settings {
	/** Applies to sessions created from now on; never to an existing one. */
	defaultSessionMode: SessionMode;
	/** Agents that can run in chat mode today. Empty means chat is unavailable. */
	chatHarnesses: string[];
	/** Deployment client identity (AO_CLIENT); empty when unset. */
	client: string;
	/** Whether the local offering is available on this daemon. */
	localEnabled: boolean;
	/** The user's persisted cloud toggle (Settings, Developer Mode). */
	cloudOffering: boolean;
	/** Whether the cloud offering is available (flag + entitled client + control plane). */
	cloudEnabled: boolean;
	/** Cloud control plane base URL; empty when cloud is not configured. */
	cloudControlPlaneUrl: string;
}

export function useSettings() {
	const query = useQuery({
		queryKey: settingsQueryKey,
		// Settings gate the cloud sign-in UI, so this query must recover from a
		// transient startup failure. The daemon can still be booting on first
		// fetch ("AO daemon is starting"); without a refetch the whole cloud
		// offering stays hidden until a manual reload. Poll like the workspace
		// query so it self-heals once the daemon is ready.
		refetchInterval: 15_000,
		retry: 5,
		queryFn: async (): Promise<Settings> => {
			const { data, error } = await apiClient.GET("/api/v1/settings");
			if (error) throw error;
			return {
				defaultSessionMode: (data?.defaultSessionMode ?? "tui") as SessionMode,
				chatHarnesses: data?.chatHarnesses ?? [],
				client: data?.client ?? "",
				// Offering gates fail closed for cloud and open for local, so a daemon
				// that predates them behaves like a plain local install.
				localEnabled: data?.localEnabled ?? true,
				cloudOffering: data?.cloudOffering ?? false,
				cloudEnabled: data?.cloudEnabled ?? false,
				cloudControlPlaneUrl: data?.cloudControlPlaneUrl ?? "",
			};
		},
	});

	return {
		settings: query.data,
		isLoading: query.isLoading,
		error: query.error ? apiErrorMessage(query.error) : undefined,
	};
}

export function useUpdateSessionInterface() {
	const queryClient = useQueryClient();
	const mutation = useMutation({
		mutationFn: async (mode: SessionMode) => {
			const { data, error } = await apiClient.PATCH("/api/v1/settings/session-interface", {
				body: { defaultSessionMode: mode },
			});
			if (error) throw error;
			return data;
		},
		// Refetch rather than writing the value in locally: the daemon is the source
		// of truth, and the control must not claim a change it did not persist.
		onSuccess: () => queryClient.invalidateQueries({ queryKey: settingsQueryKey }),
	});

	return {
		update: (mode: SessionMode) => mutation.mutate(mode),
		saving: mutation.isPending,
		error: mutation.error ? apiErrorMessage(mutation.error) : undefined,
	};
}

export function useUpdateCloudOffering() {
	const queryClient = useQueryClient();
	const mutation = useMutation({
		mutationFn: async (enabled: boolean) => {
			const { data, error } = await apiClient.PATCH("/api/v1/settings/cloud-offering", {
				body: { enabled },
			});
			if (error) throw error;
			return data;
		},
		// Refetch rather than writing the value in locally: the daemon is the source
		// of truth, and the control must not claim a change it did not persist.
		onSuccess: () => queryClient.invalidateQueries({ queryKey: settingsQueryKey }),
	});

	return {
		update: (enabled: boolean) => mutation.mutate(enabled),
		saving: mutation.isPending,
		error: mutation.error ? apiErrorMessage(mutation.error) : undefined,
	};
}
