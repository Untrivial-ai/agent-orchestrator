import { userFacingError } from "../connectionError";

export async function runSheetMutation<T>(
	action: () => Promise<T>,
	onSuccess: (value: T) => void,
	onError: (message: string) => void,
): Promise<void> {
	try {
		onSuccess(await action());
	} catch (cause) {
		onError(userFacingError(cause));
	}
}
