import path from "node:path";
import { pathToFileURL } from "node:url";

export function resolveWeeklyDate({ eventName, requestedDate, now = new Date() }) {
	if (requestedDate) return requestedDate;

	const resolved = new Date(now);
	if (Number.isNaN(resolved.getTime())) {
		throw new Error("A valid current date is required");
	}
	if (eventName === "schedule") {
		resolved.setUTCDate(resolved.getUTCDate() - 1);
	}

	return resolved.toISOString().slice(0, 10);
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
	console.log(
		resolveWeeklyDate({
			eventName: process.env.EVENT_NAME,
			requestedDate: process.env.REQUESTED_DATE,
		}),
	);
}
