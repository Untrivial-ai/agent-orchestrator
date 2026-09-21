import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

/**
 * A fresh, retry-free QueryClient for a single test render. Chat surfaces such as
 * the turn changed-files card now read workspace data through react-query, so any
 * harness that mounts them needs a client in the tree even when the query is
 * disabled and never fetches. Retries are off so error paths fail fast.
 */
export function createTestQueryClient(): QueryClient {
	return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

/** Wraps `children` in a per-instance test QueryClient. */
export function TestQueryClientProvider({
	children,
	client,
}: {
	children: ReactNode;
	client?: QueryClient;
}) {
	return <QueryClientProvider client={client ?? createTestQueryClient()}>{children}</QueryClientProvider>;
}
