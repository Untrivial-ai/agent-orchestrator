/**
 * The agent's plan for a turn, as a live checklist.
 *
 * One panel per turn, not a row per revision. The provider re-sends the whole plan
 * every time it changes — a dozen times in a long turn — and the daemon overwrites
 * the turn's copy rather than appending, so this is current state. Rendering each
 * revision would read as though the agent had planned a dozen separate times.
 *
 * Position is load-bearing. It sits at the end of its turn, next to the changed
 * files, because both answer "where has this turn got to" — and because a panel that
 * grows at the end of a turn pushes nothing the reader is looking at. Ticking a step
 * changes no layout at all: the row keeps its height and only the mark and the tone
 * change, so a plan settling under a reader's eyes never moves the text they are on.
 */

import { memo, useState } from "react";
import { Check, ListChecks, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import type { ConversationPlan, PlanStepStatus } from "../../types/conversation";

const STEP_LABEL: Record<PlanStepStatus, string> = {
	pending: "not started",
	in_progress: "in progress",
	completed: "done",
};

export const TurnPlan = memo(function TurnPlan({
	plan,
	live,
}: {
	plan: ConversationPlan;
	/** The turn is still running, so the plan can still change. */
	live?: boolean;
}) {
	const [expanded, setExpanded] = useState(true);
	if (plan.steps.length === 0) return null;
	const done = plan.steps.filter((step) => step.status === "completed").length;

	return (
		<section
			aria-label="Agent plan"
			className="rounded-lg border border-border bg-surface"
			data-turn-plan="true"
		>
			<div className="flex items-center gap-2 px-3.5 py-2.5">
				<button
					type="button"
					onClick={() => setExpanded((open) => !open)}
					aria-expanded={expanded}
					aria-label={expanded ? "Collapse plan" : "Expand plan"}
					className="inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
				>
					<ListChecks aria-hidden="true" className="size-4" />
				</button>
				<strong className="shrink-0 text-xs font-semibold text-foreground">Plan</strong>
				{live && done < plan.steps.length ? (
					<Loader2
						aria-label="still planning"
						className="size-3 shrink-0 animate-spin text-muted-foreground/60"
					/>
				) : null}
				<span className="flex-1" />
				{/* Tabular so the count does not jitter the header as it climbs. */}
				<span className="shrink-0 font-mono text-[10.5px] tabular-nums text-muted-foreground">
					{done}/{plan.steps.length}
				</span>
			</div>

			{expanded ? (
				<>
					{plan.explanation ? (
						<p className="px-3.5 pb-2 text-[11px] leading-relaxed text-muted-foreground">
							{plan.explanation}
						</p>
					) : null}

					{/* A real list, so a screen reader hears "list, 5 items" and the status of each
					    step is spoken rather than left to a colour. */}
					<ol className="flex flex-col border-t border-border py-1">
						{plan.steps.map((step, index) => (
							<li
								key={`${index}-${step.text}`}
								className="flex items-start gap-2.5 px-3.5 py-1"
								aria-label={`${step.text} — ${STEP_LABEL[step.status]}`}
							>
								<StepMark status={step.status} />
								<span
									className={cn(
										"min-w-0 flex-1 text-[11.5px] leading-[1.45]",
										step.status === "completed" && "text-muted-foreground/70 line-through",
										step.status === "in_progress" && "text-foreground",
										step.status === "pending" && "text-muted-foreground",
									)}
								>
									{step.text}
								</span>
							</li>
						))}
					</ol>
				</>
			) : null}
		</section>
	);
});

/**
 * The mark for one step, in a fixed-size slot.
 *
 * Same box whatever the state, because these change while the reader is looking at
 * them: a spinner that is a pixel taller than a tick would nudge every line below it
 * each time a step finished.
 */
function StepMark({ status }: { status: PlanStepStatus }) {
	return (
		<span
			aria-hidden="true"
			className="mt-[3px] flex size-3 shrink-0 items-center justify-center"
		>
			{status === "completed" ? (
				<Check className="size-3 text-success" />
			) : status === "in_progress" ? (
				<Loader2 className="size-3 animate-spin text-accent" />
			) : (
				<span className="size-[7px] rounded-full border border-border-strong" />
			)}
		</span>
	);
}
