import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
	defaultOptions: {
		queries: {
			staleTime: 10_000,
			refetchOnWindowFocus: false,
			// AO talks to a localhost daemon, so its queries must run regardless of
			// the browser's online flag. React Query's default (networkMode
			// "online") pauses every query when navigator.onLine is false, which on
			// a flaky internet connection strands even local reads (e.g. settings,
			// which gates the cloud sign-in UI) in a perpetual loading state.
			networkMode: "always",
		},
		mutations: {
			networkMode: "always",
		},
	},
});
