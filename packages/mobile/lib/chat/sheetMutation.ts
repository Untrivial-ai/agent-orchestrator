export async function runSheetMutation<T>(
	action: () => Promise<T>,
	onSuccess: (value: T) => void,
	onError: (message: string) => void,
): Promise<void> {
	try {
		onSuccess(await action());
	} catch (cause) {
		onError(cause instanceof Error ? cause.message : String(cause));
	}
}
