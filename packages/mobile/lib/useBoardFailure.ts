import { useMemo } from "react";
import { Platform } from "react-native";
import { classifyConnectionFailure, describeConnectionFailure, type ConnectionErrorCopy } from "./connectionError";
import { tunnelMayHaveRotated } from "./staleTunnel";
import { useApp } from "./store";

/**
 * The board poll's failure as human copy, shared by every tab so the same
 * failure reads the same on Workers, Projects and PRs.
 */
export function useBoardFailure(): ConnectionErrorCopy {
	const { errorStatus, connection, config, activeEndpoints } = useApp();
	return useMemo(() => {
		const classified = classifyConnectionFailure(errorStatus ?? undefined);
		return describeConnectionFailure(
			// A stored tunnel that no longer answers means the hostname rotated,
			// which no amount of retrying fixes — rescanning does. Distinguished
			// here rather than in the classifier because it depends on what the
			// machine advertised, not on a status code.
			classified === "unreachable" && tunnelMayHaveRotated(activeEndpoints, config?.endpointKind, connection === "open")
				? "tunnel-rotated"
				: classified,
			{ host: config?.host ?? "", port: config?.httpPort ?? "", platform: Platform.OS },
		);
	}, [errorStatus, config?.host, config?.httpPort, activeEndpoints, connection]);
}
