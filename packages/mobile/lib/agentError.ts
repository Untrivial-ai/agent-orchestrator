import { userFacingError } from "./connectionError";

// Human copy for a failed agent-catalog fetch, shared by the spawn screen and the
// agent sheet route. It used to return only the connection copy's title ("Your
// desktop disconnected"), which read as a fragment with no next step.
export function agentErrorCopy(e: unknown): string {
	return userFacingError(e, "Couldn't load your agents. Try again.");
}
