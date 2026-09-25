/**
 * Conversation state that is not a timeline entry.
 *
 * Each of these answers a question the timeline structurally cannot. A tool server
 * that failed to start produces no rows at all — the agent simply never calls those
 * tools, which reads as a choice. A provider demanding credentials leaves every
 * later turn failing for a reason that looks generic. A thread the provider has put
 * into `system_error` looks, from AO's side, like an agent that has gone quiet.
 *
 * Persistent recovery states live above the scroller; the transient MCP note docks
 * below the composer so it does not displace the conversation.
 */

import { memo, useEffect, useState } from "react";
import { KeyRound, Plug, TriangleAlert } from "lucide-react";
import { motion, useReducedMotion } from "motion/react";
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
	reasonInTimeline = false,
}: {
	account: ConversationAccount;
	harness: string;
	reasonInTimeline?: boolean;
}) {
	if (!account.reauthRequiredAt) return null;
	const command = signInCommand(harness);

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
				{!reasonInTimeline ? (
					<p className="text-[11px] leading-relaxed text-foreground">
						{account.reauthReason ??
							"The provider rejected this session's credentials."}{" "}
						Nothing will run until it is fixed, and the worktree is untouched.
					</p>
				) : null}
				<p className="text-[11px] leading-relaxed text-muted-foreground">
					{command ? (
						<>
							Run{" "}
							<code className="rounded bg-background px-1 py-0.5 font-mono text-[10.5px] text-foreground">
								{command}
							</code>{" "}
							in a terminal, then send your message again. AO holds no credentials of its own.
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
 * A short-lived acknowledgement that some tool servers are unavailable.
 *
 * The server setup lives with the agent harness, not AO. Show the fact without a
 * noisy diagnostic panel or a misleading recovery control, then get out of the way.
 */
export const McpServerBanner = memo(function McpServerBanner({
	servers,
}: {
	/** Only the broken ones. The caller filters, so an empty list means nothing to say. */
	servers: McpServer[];
}) {
	const fingerprint = servers
		.map((server) => `${server.name}:${server.status}:${server.failureReason ?? ""}:${server.error ?? ""}`)
		.join("|");
	const [dismissedFingerprint, setDismissedFingerprint] = useState<string | null>(null);
	const [dismissingFingerprint, setDismissingFingerprint] = useState<string | null>(null);
	const reducedMotion = useReducedMotion();

	useEffect(() => {
		if (!fingerprint) return;
		const timeout = window.setTimeout(() => setDismissingFingerprint(fingerprint), 3_500);
		return () => window.clearTimeout(timeout);
	}, [fingerprint]);

	useEffect(() => {
		if (dismissingFingerprint !== fingerprint) return;
		const timeout = window.setTimeout(() => setDismissedFingerprint(fingerprint), reducedMotion ? 0 : 200);
		return () => window.clearTimeout(timeout);
	}, [dismissingFingerprint, fingerprint, reducedMotion]);

	const visible = servers.length > 0 && dismissedFingerprint !== fingerprint;
	const dismissing = dismissingFingerprint === fingerprint;
	const serverNames = servers
		.map((server) => `${server.name.slice(0, 1).toUpperCase()}${server.name.slice(1)}`)
		.join(", ");
	const message = `${serverNames} ${servers.length === 1 ? "MCP" : "MCPs"} unavailable`;

	if (!visible) return null;

	return (
		<motion.div
			initial={{ scale: 0.96, opacity: 0 }}
			animate={dismissing ? { scale: 0.96, opacity: 0 } : { scale: 1, opacity: 1 }}
			transition={{ duration: reducedMotion ? 0 : 0.2, ease: [0.22, 1, 0.36, 1] }}
			className="absolute bottom-full left-1/2 w-fit -translate-x-1/2 origin-center pb-2"
		>
			<div
				role={dismissing ? undefined : "status"}
				aria-atomic="true"
				className="flex items-center gap-1.5 px-1 text-[11px] text-muted-foreground"
			>
				<Plug aria-hidden="true" className="size-3 shrink-0 text-warning" />
				<span>{message}</span>
			</div>
		</motion.div>
	);
});
