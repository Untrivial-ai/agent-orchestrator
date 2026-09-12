import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createCue,
	deleteCue,
	fetchProjectCues,
	invokeCue,
	projectCuesQueryKey,
	updateCue,
	type CreateCueInput,
	type UpdateCueInput,
} from "../lib/cues";

export function useProjectCuesQuery(projectId: string, enabled = true) {
	return useQuery({
		queryKey: projectCuesQueryKey(projectId),
		queryFn: () => fetchProjectCues(projectId),
		enabled: enabled && Boolean(projectId),
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
		mutationFn: (input: CreateCueInput) => createCue(projectId, input),
		onSuccess: invalidate,
	});
}

export function useUpdateCueMutation(projectId: string) {
	const invalidate = useInvalidateCues(projectId);
	return useMutation({
		mutationFn: ({ cueId, input }: { cueId: string; input: UpdateCueInput }) => updateCue(cueId, input),
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
		mutationFn: ({ cueId, sessionId }: { cueId: string; sessionId?: string }) => invokeCue(cueId, sessionId),
	});
}