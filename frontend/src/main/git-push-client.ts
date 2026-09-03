import { net } from "electron";

export async function callGitPushBroker<T>(
	port: number,
	capability: string,
	action: "prepare" | "approve-and-push" | "revoke",
	body: unknown,
): Promise<T> {
	const response = await net.fetch(`http://127.0.0.1:${port}/internal/git-push/${action}`, {
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			"X-AO-Git-Push-Capability": capability,
		},
		body: JSON.stringify(body),
	});
	if (!response.ok) {
		let message = `Git push broker rejected the request (${response.status})`;
		try {
			const payload = (await response.json()) as { error?: { message?: string }; message?: string };
			message = payload.error?.message ?? payload.message ?? message;
		} catch { /* retain bounded status error */ }
		throw new Error(message);
	}
	if (response.status === 204) return undefined as T;
	return (await response.json()) as T;
}
