import { useCallback, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

type PreparedClone = components["schemas"]["ClonePreparationResult"];

export function usePreparedClone() {
	const currentRef = useRef<PreparedClone | null>(null);
	const [isCleaning, setIsCleaning] = useState(false);

	const prepare = useCallback(async (remoteUrl: string, destinationParent: string) => {
		const { data, error } = await apiClient.POST("/api/v1/projects/clone/prepare", {
			body: { remoteUrl, destinationParent },
		});
		if (error || !data) throw new Error(apiErrorMessage(error, "Could not clone repository"));
		currentRef.current = data;
		return data;
	}, []);

	const cleanup = useCallback(async () => {
		const current = currentRef.current;
		if (!current) return;
		setIsCleaning(true);
		try {
			const { error } = await apiClient.POST("/api/v1/projects/clone/cleanup", {
				body: { path: current.path, preparationId: current.preparationId },
			});
			if (error) throw new Error(apiErrorMessage(error, "Could not clean up prepared clone"));
			currentRef.current = null;
		} finally {
			setIsCleaning(false);
		}
	}, []);

	const complete = useCallback(() => {
		currentRef.current = null;
	}, []);

	const current = useCallback(() => currentRef.current, []);

	return { cleanup, complete, current, isCleaning, prepare };
}
