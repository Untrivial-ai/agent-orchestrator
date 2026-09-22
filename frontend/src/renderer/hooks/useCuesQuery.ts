import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createCue,
	deleteCue,
	fetchProjectCues,
	invokeCue,
	projectCuesQueryKey,
	updateCue,
	type CueInput,
} from "../lib/cues";

export function useProjectCuesQuery(projectId: string, enabled = true) {
	return useQuery({
		queryKey: projectCuesQueryKey(projectId),
		queryFn: () => fetchProjectCues(projectId),
		enabled: enabled && Boolean(projectId),
		refetchOnMount: "always",
		retry: false,
	});
}

function useInvalidateCues(projectId: string) {
	const queryClient = useQueryClient();
	return () => {
		void queryClient.invalidateQueries({ queryKey: projectCuesQueryKey(projectId) });
	};
}

export function useCreateCueMutation(projectId: string) {
	const invalidate = useInvalidateCues(projectId);
	return useMutation({
		mutationFn: (input: CueInput) => createCue(projectId, input),
		onSuccess: invalidate,
	});
}

export function useUpdateCueMutation(projectId: string) {
	const invalidate = useInvalidateCues(projectId);
	return useMutation({
		mutationFn: ({ cueId, input }: { cueId: string; input: CueInput }) => updateCue(cueId, input),
		onSuccess: invalidate,
	});
}

export function useDeleteCueMutation(projectId: string) {
	const invalidate = useInvalidateCues(projectId);
	return useMutation({
		mutationFn: (cueId: string) => deleteCue(cueId),
		onSuccess: invalidate,
	});
}

export function useInvokeCueMutation() {
	return useMutation({
		mutationFn: ({ cueId, sessionId, shell }: { cueId: string; sessionId?: string; shell?: string }) => invokeCue(cueId, sessionId, shell),
		retry: false,
	});
}
