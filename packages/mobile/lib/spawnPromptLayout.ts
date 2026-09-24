/** The controls translate by the keyboard height without changing layout. */
export function availablePromptHeight(room: number, keyboardHeight: number): number {
	return Math.max(0, room - Math.max(0, keyboardHeight));
}
