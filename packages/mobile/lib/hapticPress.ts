export function withHaptic<TArgs extends unknown[], TResult>(
	feedback: () => void,
	action: (...args: TArgs) => TResult,
): (...args: TArgs) => TResult {
	return (...args) => {
		feedback();
		return action(...args);
	};
}
