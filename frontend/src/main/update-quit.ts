/** Squirrel has already persisted install + relaunch before this is called. */
export function finishUpdateQuit(
	cleanup: Promise<unknown>,
	actions: { quit(): void; exit(): void; log(error: unknown): void },
	timeoutMs = 15_000,
): Promise<void> {
	return new Promise((resolve) => {
		let finished = false;
		const finish = (forced: boolean, error?: unknown) => {
			if (finished) return;
			finished = true;
			clearTimeout(timer);
			if (error) actions.log(error);
			if (forced) actions.exit();
			else actions.quit();
			resolve();
		};
		const timer = setTimeout(() => finish(true, new Error("Update shutdown cleanup exceeded 15 seconds; exiting AO so the prepared installer can continue.")), timeoutMs);
		cleanup.then(() => finish(false), (error) => finish(false, error));
	});
}
