import { useEffect, useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { queryClient } from "../lib/query-client";

export type LinkPreview = components["schemas"]["LinkPreviewResponse"];

export function linkPreviewQueryKey(url: string) {
	return ["link-preview", url] as const;
}

async function fetchLinkPreview(url: string): Promise<LinkPreview> {
	const { data, error } = await apiClient.GET("/api/v1/link-preview", {
		params: { query: { url } },
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not load link preview"));
	return data;
}

type LinkPreviewResult = {
	url: string;
	data?: LinkPreview;
	isError: boolean;
};

/**
 * Fetches Open Graph metadata for an external URL through the daemon (the
 * renderer CSP blocks direct external fetches). Lazy: pass `enabled` only when
 * the preview is actually being shown.
 *
 * Uses the shared queryClient imperatively rather than useQuery so AppLink —
 * which renders in many provider-less contexts — stays safe everywhere. The
 * cache/dedupe semantics are identical: one entry per URL, fresh for the
 * session. Errors surface as `isError` and mean "no preview" for the current
 * hover; they are cleared when the hook is disabled so the next hover retries.
 *
 * Results are keyed by URL: callers like XtermTerminal reuse one hook instance
 * while the hovered URL changes, so state from a previous URL must never be
 * returned for the current one.
 */
export function useLinkPreview(url: string, enabled: boolean) {
	const [result, setResult] = useState<LinkPreviewResult>(() => ({
		url,
		data: queryClient.getQueryData<LinkPreview>(linkPreviewQueryKey(url)),
		isError: false,
	}));

	useEffect(() => {
		if (!enabled) {
			// A failure only suppresses the preview for the current hover. Clearing it
			// on disable lets the next hover retry — transient daemon/upstream blips
			// must not kill previews for this component's lifetime. Successes are
			// untouched: they keep serving from the queryClient cache below.
			if (result.url === url && result.isError) setResult({ url, isError: false });
			return;
		}
		if (result.url === url && (result.data !== undefined || result.isError)) return;
		if (queryClient.getQueryData<LinkPreview>(linkPreviewQueryKey(url)) !== undefined) {
			setResult({ url, data: queryClient.getQueryData<LinkPreview>(linkPreviewQueryKey(url)), isError: false });
			return;
		}
		let cancelled = false;
		queryClient
			.fetchQuery({
				queryKey: linkPreviewQueryKey(url),
				queryFn: () => fetchLinkPreview(url),
				staleTime: Number.POSITIVE_INFINITY,
				retry: false,
			})
			.then((preview) => {
				if (cancelled) return;
				setResult({ url, data: preview, isError: false });
			})
			.catch(() => {
				if (cancelled) return;
				setResult({ url, isError: true });
			});
		return () => {
			cancelled = true;
		};
	}, [enabled, url, result]);

	const current: LinkPreviewResult =
		result.url === url
			? result
			: { url, data: queryClient.getQueryData<LinkPreview>(linkPreviewQueryKey(url)), isError: false };
	return {
		data: current.data,
		isPending: enabled && current.data === undefined && !current.isError,
		isError: current.isError,
	};
}
