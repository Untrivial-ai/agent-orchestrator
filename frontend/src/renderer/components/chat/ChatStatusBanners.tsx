/**
 * Conversation state that is not a timeline entry.
 *
 * Each of these answers a question the timeline structurally cannot. A tool server
 * that failed to start produces no rows at all — the agent simply never calls those
 * tools, which reads as a choice. A provider demanding credentials leaves every
 * later turn failing for a reason that looks generic. A thread the provider has put
 * into `system_error` looks, from AO's side, like an agent that has gone quiet.
 *
 * They live above the scroller rather than in it because they are current state:
 * scrolling away from them must not scroll away from the reason the session is
 * stuck.
 */

import { memo } from "react";
import { KeyRound, Plug, RefreshCw, TriangleAlert } from "lucide-react";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import type { ConversationAccount, ConversationThreadState, McpServer } from "../../types/conversation";

/**
 * The provider will not do any more work until someone signs in.
 *
 * The loudest thing on the surface, on purpose: nothing else the user does will
 * help, and every turn they send until they fix it will fail. It names the command
 * because "re-authenticate" is not an action anyone can take — the credentials live
 * with the agent's own CLI, not with AO, which is exactly why the daemon could not
 * fix this itself.
 */
export const ReauthBanner = memo(function ReauthBanner({
	account,
	harness,
}: {
	account: ConversationAccount;
	harness: string;
}) {
	if (!account.reauthRequiredAt) return null;
	const command = signInCommand(harness);
	const nextStep =
		harness === "codex" ? "resume the agent and resend your message" : "send your message again";

	return (
		<div
			role="alert"
			className="flex shrink-0 items-start gap-2.5 border-b border-destructive/40 bg-destructive/10 px-4 py-3"
		>
			<KeyRound aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
			<div className="flex min-w-0 flex-col gap-1">
				<strong className="text-xs font-semibold text-destructive">
					Sign in again to keep going
				</strong>
				<p className="text-[11px] leading-relaxed text-foreground">
					{account.reauthReason ??
						"The provider rejected this session's credentials."}{" "}
					Nothing will run until it is fixed, and the worktree is untouched.
				</p>
				<p className="text-[11px] leading-relaxed text-muted-foreground">
					{command ? (
						<>
							Run{" "}
							<code className="rounded bg-background px-1 py-0.5 font-mono text-[10.5px] text-foreground">
								{command}
							</code>{" "}
							in a terminal, then {nextStep}. AO holds no credentials of its own.
						</>
					) : (
						<>
							Sign in with the agent&rsquo;s own CLI, then send your message again. AO holds no
							credentials of its own.
						</>
					)}
				</p>
			</div>
		</div>
	);
});

/**
 * The sign-in command for a harness, or nothing.
 *
 * Named per harness rather than described generically, because a user staring at a
 * blocked session wants the line to type. Unknown harnesses get the generic wording
 * instead of a guessed command that would fail.
 */
function signInCommand(harness: string): string | undefined {
	switch (harness) {
		case "codex":
			return "codex login";
		case "claude-code":
		case "claude":
			return "claude auth login";
		default:
			return undefined;
	}
}

/**
 * The provider's own view of the thread, when it is bad.
 *
 * Deliberately separate from the controller banner and worded so the two cannot be
 * confused: the controller is AO's connection to the agent process, this is what the
 * provider says about the conversation behind it. They disagree routinely — a
 * healthy controller can be attached to a thread the provider has already given up
 * on, and that combination is precisely the one a user cannot diagnose unaided.
 *
 * Only `system_error` and `closed` are drawn. `active`, `idle` and `not_loaded` are
 * the ordinary run of a session and a banner for each would be noise that teaches
 * readers to ignore this row.
 */
export const ThreadStateBanner = memo(function ThreadStateBanner({
	threadState,
}: {
	threadState: ConversationThreadState;
}) {
	const status = threadState.status;
	if (status !== "system_error" && status !== "closed") return null;

	const copy =
		status === "system_error"
			? {
					title: "The agent's thread hit an internal error",
					body: "The provider reported a fault in this thread, not in AO's connection to it. New turns will usually fail; the conversation and the worktree are kept.",
				}
			: {
					title: "The agent closed this thread",
					body: "The provider dropped the conversation on its side. AO kept the history, but the agent no longer holds it.",
				};

	return (
		<div
			role="alert"
			aria-atomic="true"
			className="flex shrink-0 items-start gap-2.5 border-b border-border bg-surface px-4 py-2.5"
		>
			<TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-warning" />
			<div className="flex min-w-0 flex-col gap-0.5">
				<strong className="text-xs font-medium text-warning">{copy.title}</strong>
				<span className="text-[11px] leading-snug text-muted-foreground">{copy.body}</span>
				{threadState.waitingOn?.length ? (
					<span className="text-[11px] leading-snug text-muted-foreground">
						Waiting on: {threadState.waitingOn.join(", ")}
					</span>
				) : null}
			</div>
		</div>
	);
});

/**
 * Tool servers that did not start.
 *
 * Only failures are shown. A healthy server is not news, and listing every one would
 * put a permanent status bar above a conversation to say that nothing is wrong. A
 * failed one is worth interrupting for because its absence is invisible: the agent
 * will not mention the tools it does not have, so the user sees a worse answer with
 * no cause.
 */
export const McpServerBanner = memo(function McpServerBanner({
	servers,
	onReload,
	reloading,
	turnInFlight,
	error,
}: {
	/** Only the broken ones. The caller filters, so an empty list means nothing to say. */
	servers: McpServer[];
	/** Absent when the harness cannot reload, in which case no control is drawn. */
	onReload?: () => void;
	reloading?: boolean;
	/** The daemon refuses a reload mid-turn, so the control explains itself instead. */
	turnInFlight?: boolean;
	error?: string;
}) {
	if (servers.length === 0) return null;

	return (
		<div
			role="status"
			aria-atomic="true"
			className="flex shrink-0 items-start gap-2.5 border-b border-border bg-surface px-4 py-2.5"
		>
			<Plug aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-warning" />
			<div className="flex min-w-0 flex-1 flex-col gap-1">
				<strong className="text-xs font-medium text-warning">
					{servers.length === 1
						? "A tool server did not start"
						: `${servers.length} tool servers did not start`}
				</strong>
				<span className="text-[11px] leading-snug text-muted-foreground">
					The agent has none of their tools and will not say so — it works around them
					silently.
				</span>
				<ul className="flex flex-col gap-0.5">
					{servers.map((server) => (
						<li key={server.name} className="text-[11px] leading-snug">
							<span className="font-mono text-foreground">{server.name}</span>
							<span className="text-muted-foreground">
								{" · "}
								{server.status}
								{/* The classification first, then the raw text: one is actionable,
								    the other is the provider's own words and often long. */}
								{server.failureReason ? ` · ${server.failureReason}` : ""}
							</span>
							{server.error ? (
								<span className="block truncate text-[10.5px] text-muted-foreground/70" title={server.error}>
									{server.error}
								</span>
							) : null}
						</li>
					))}
				</ul>
				{error ? <span className="text-[11px] text-destructive">{error}</span> : null}
			</div>
			{onReload ? (
				<Button
					type="button"
					size="sm"
					variant="outline"
					onClick={onReload}
					disabled={reloading || turnInFlight}
					title={
						turnInFlight
							? "Finish or stop the current turn before reloading tool servers"
							: "Start the tool servers again"
					}
					className="shrink-0 gap-1.5"
				>
					<RefreshCw
						aria-hidden="true"
						className={cn("size-3", reloading && "animate-spin")}
					/>
					{reloading ? "Reloading…" : "Reload"}
				</Button>
			) : null}
		</div>
	);
});
