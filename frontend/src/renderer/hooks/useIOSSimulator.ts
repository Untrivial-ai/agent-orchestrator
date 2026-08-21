import { useQuery, useMutation } from "@tanstack/react-query";
import { useEffect } from "react";

import { apiClient, apiErrorMessage } from "../lib/api-client";

/**
 * Hook for iOS Simulator toolchain status, modelled after
 * useAndroidDevice(enabled: boolean). It is only enabled when
 * isMacPlatform() is true (frontend/src/renderer/lib/platform.ts).
 *
 * Returns status polling plus mutation methods for start/stop/fetch-runtime.
 */
export function useIOSSimulator(enabled: boolean) {
	const status = useQuery({
		queryKey: ["ios-device", "toolchain", "status"],
		queryFn: async () => {
			const resp = await apiClient.GET("/api/v1/ios-device/toolchain/status");
			if (resp.error) throw new Error(apiErrorMessage(resp.error, "Failed to fetch iOS toolchain status"));
			return resp.data;
		},
		enabled: enabled,
		refetchInterval: false,
	});

	const start = useMutation({
		mutationFn: async () => {
			const resp = await apiClient.POST("/api/v1/ios-device/toolchain/recheck");
			if (resp.error) throw new Error(apiErrorMessage(resp.error, "Failed to recheck iOS toolchain"));
			return resp.data;
		},
	});

	const stop = useMutation({
		mutationFn: async () => {
			// "Stop" for iOS simulator currently means re-evaluating the install state.
			// A full stop/boot lifecycle is B3/B5 scope; for now recheck.
			const resp = await apiClient.POST("/api/v1/ios-device/toolchain/recheck");
			if (resp.error) throw new Error(apiErrorMessage(resp.error, "Failed to recheck iOS toolchain"));
			return resp.data;
		},
	});

	// Derive human-readable flags for the renderer.
	const isXcodeDetected = status.data?.xcodeDetected ?? false;
	const isCLTOnly = status.data?.cltOnly ?? false;
	const hasSimctl = status.data?.simctlAvailable ?? false;
	const hasDefaultRuntime = status.data?.defaultRuntimeAvailable ?? false;

	useEffect(() => {
		// Reserved for debug logging once the full B3/B5 flow is wired.
		void { isXcodeDetected, isCLTOnly, hasSimctl, hasDefaultRuntime };
	}, [status.data, isXcodeDetected, isCLTOnly, hasSimctl, hasDefaultRuntime]);

	return {
		// Status flags.
		isXcodeDetected,
		isCLTOnly,
		hasSimctl,
		hasDefaultRuntime,

		// React Query status.
		isLoading: status.isLoading,
		isError: status.isError,
		error: status.error,

		// Mutations.
		start,
		stop,
	};
}