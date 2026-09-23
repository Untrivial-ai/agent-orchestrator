/**
 * A run of consecutive tool calls, collapsed to one line.
 *
 * The agent may run fifteen commands to answer one question. Rendering each as
 * its own row turns the conversation into a log: the prose that actually answers
 * the question gets pushed off screen by the mechanics of finding it.
 *
 * So a run summarizes itself — "Explored 4 files, 3 searches" — and expands to the
 * individual calls only when asked. The summary is not decoration: it counts what
 * the agent did by category, so a reader can tell "it looked around" from "it
 * changed something" without opening anything.
 */

import { useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { ChevronRight, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { ActivityRow, ActivityTransition } from "./ChatTimelineItems";
import {
	ACTIVITY_SUMMARY_BUTTON_CLASS,
	commandCategory,
	isNonzeroCommandExit,
} from "./activity-command";
import { fileChangeFiles, type ConversationActivity } from "../../types/conversation";

export function ActivityRun({ activities }: { activities: ConversationActivity[] }) {
	// null until someone decides, so a run holding a command that is printing right
	// now can open itself and close again once everything settles. A click pins the
	// choice either way.
	const [override, setOverride] = useState<boolean | null>(null);
	const reducedMotion = useReducedMotion();
	const running = activities.some((a) => a.status === "running");
	const nonzeroExits = activities.filter(isNonzeroCommandExit).length;
	const failed = activities.filter(
		(activity) => activity.status === "failed" && !isNonzeroCommandExit(activity),
	).length;
	const cancelled = activities.filter((a) => a.status === "cancelled").length;
	const diffTotals = activities.reduce(
		(totals, activity) => {
			if (activity.activityKind !== "file_change") return totals;
			for (const file of fileChangeFiles(activity)) {
				totals.additions += file.additions;
				totals.deletions += file.deletions;
			}
			return totals;
		},
		{ additions: 0, deletions: 0 },
	);

	// A single call is its own best summary — collapsing one row into a count of
	// one would be worse than just showing it.
	const hierarchy = buildHierarchy(activities);
	const nodesByActivityID = new Map(hierarchy.map((node) => [node.activity.id, node]));
	const rootActivities = hierarchy.map((node) => node.activity);
	const subgroups = groupSimilarActivities(rootActivities);
	const summary = summarize(activities);
	if (activities.length === 1 && hierarchy[0]?.children.length === 0) {
		return <ActivityRow activity={activities[0]!} />;
	}

	// Otherwise a command streaming output inside a run stays hidden behind this
	// summary line, and the live output is live to nobody.
	const streamingOutput = activities.some((a) => a.status === "running" && Boolean(a.detail?.output));
	const open = override ?? streamingOutput;

	return (
		<motion.div
			initial={reducedMotion ? false : { opacity: 0 }}
			animate={{ opacity: 1 }}
			transition={{ duration: reducedMotion ? 0 : 0.2, ease: [0.22, 1, 0.36, 1] }}
			className="flex flex-col"
		>
			<button
				type="button"
				onClick={() => setOverride(!open)}
				aria-expanded={open}
				className={cn(ACTIVITY_SUMMARY_BUTTON_CLASS, "activity-run-toggle")}
			>
				<span className="activity-summary-label text-[11.5px] text-muted-foreground">
					<ActivityTransition value={summary} inline>{summary}</ActivityTransition>
				</span>
				{nonzeroExits > 0 ? (
					<span className="text-[11px] text-muted-foreground/70">
						{nonzeroExits} exited
					</span>
				) : null}
				{failed > 0 ? (
					<span className="text-[11px] text-destructive">
						{failed} failed
					</span>
				) : null}
				{cancelled > 0 ? (
					<span className="text-[11px] text-muted-foreground/70">
						{cancelled} stopped
					</span>
				) : null}
				{diffTotals.additions > 0 || diffTotals.deletions > 0 ? (
					<span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70">
						{diffTotals.additions > 0 ? (
							<span className="text-success">+{diffTotals.additions}</span>
						) : null}
						{diffTotals.additions > 0 && diffTotals.deletions > 0 ? " " : null}
						{diffTotals.deletions > 0 ? (
							<span className="text-destructive">−{diffTotals.deletions}</span>
						) : null}
					</span>
				) : null}
				{running ? (
					<Loader2 aria-hidden="true" className="size-3 animate-spin text-muted-foreground/60" />
				) : null}
				{/* Always visible: the line has to read as openable, or a reader who
				    wants the detail has no reason to think it is there. */}
				<ChevronRight
					aria-hidden="true"
					className={cn(
						"size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover/run:text-muted-foreground",
						open && "rotate-90",
					)}
				/>
			</button>

			<AnimatePresence initial={false}>
				{open ? (
					<motion.div
						initial={{ height: 0, opacity: 0 }}
						animate={{ height: "auto", opacity: 1 }}
						exit={{ height: 0, opacity: 0 }}
						transition={{ duration: reducedMotion ? 0 : 0.2, ease: [0.22, 1, 0.36, 1] }}
						className="overflow-hidden"
					>
						<div className="flex flex-col gap-1 pt-1">
							{subgroups.length === 1
								? subgroups[0]!.activities.map((activity) => {
										const node = nodesByActivityID.get(activity.id);
										return node ? (
											<ActivityTree key={activity.id} node={node} />
										) : (
											<ActivityRow key={activity.id} activity={activity} />
										);
								  })
								: subgroups.map((group) => (
										<ActivitySubgroup key={group.key} activities={group.activities} nodesByActivityID={nodesByActivityID} />
								  ))}
						</div>
					</motion.div>
				) : null}
			</AnimatePresence>
		</motion.div>
	);
}

type ActivitySubgroupData = { key: string; activities: ConversationActivity[] };

function groupSimilarActivities(activities: ConversationActivity[]): ActivitySubgroupData[] {
	const groups: ActivitySubgroupData[] = [];
	for (const activity of activities) {
		const key = activityGroupKey(activity);
		const previous = groups.at(-1);
		if (previous?.key === key) previous.activities.push(activity);
		else groups.push({ key, activities: [activity] });
	}
	return groups;
}

function activityGroupKey(activity: ConversationActivity): string {
	const parentSuffix = activity.detail?.parentProviderItemId ? ":nested" : "";
	if (activity.activityKind === "command") {
		return `command:${commandCategory(activity.detail?.command ?? activity.summary)}${parentSuffix}`;
	}
	return `${activity.activityKind}${parentSuffix}`;
}

function ActivitySubgroup({
	activities,
	nodesByActivityID,
}: {
	activities: ConversationActivity[];
	nodesByActivityID: ReadonlyMap<string, ActivityNode>;
}) {
	const [override, setOverride] = useState<boolean | null>(null);
	const reducedMotion = useReducedMotion();
	const streamingOutput = activities.some((activity) =>
		activity.status === "running" && Boolean(activity.detail?.output),
	);
	const open = override ?? streamingOutput;
	const hasNestedAgent = activities.some((activity) =>
		nodesByActivityID.get(activity.id)?.children.length,
	);
	const isStructural = activities.some((activity) => Boolean(activity.detail?.parentProviderItemId));
	if (activities.length === 1 || hasNestedAgent || isStructural) {
		return (
			<>
				{activities.map((activity) => {
					const node = nodesByActivityID.get(activity.id);
					return node ? (
						<ActivityTree key={activity.id} node={node} />
					) : (
						<ActivityRow key={activity.id} activity={activity} />
					);
				})}
			</>
		);
	}

	return (
		<div className="activity-subgroup">
			<button
				type="button"
				onClick={() => setOverride(!open)}
				aria-expanded={open}
				className={cn(ACTIVITY_SUMMARY_BUTTON_CLASS, "activity-subgroup-toggle")}
			>
				<span className="activity-summary-label text-[11px] text-muted-foreground">
					<ActivityTransition value={summarizeSubgroup(activities)} inline>
						{summarizeSubgroup(activities)}
					</ActivityTransition>
				</span>
				<ChevronRight aria-hidden="true" className={cn("size-3 shrink-0 transition-transform", open && "rotate-90")} />
			</button>
			<AnimatePresence initial={false}>
				{open ? (
					<motion.div
						initial={{ height: 0, opacity: 0 }}
						animate={{ height: "auto", opacity: 1 }}
						exit={{ height: 0, opacity: 0 }}
						transition={{ duration: reducedMotion ? 0 : 0.16, ease: [0.22, 1, 0.36, 1] }}
						className="overflow-hidden"
					>
						<div className="flex flex-col gap-1">
							{activities.map((activity) => {
								const node = nodesByActivityID.get(activity.id);
								return node ? (
									<ActivityTree key={activity.id} node={node} />
								) : (
									<ActivityRow key={activity.id} activity={activity} />
								);
							})}
						</div>
					</motion.div>
				) : null}
			</AnimatePresence>
		</div>
	);
}

function summarizeSubgroup(activities: ConversationActivity[]): string {
	const first = activities[0];
	if (!first) return "Related calls";
	if (first.activityKind === "file_change") {
		const files = activities.reduce(
			(count, activity) => count + Math.max(1, fileChangeFiles(activity).length),
			0,
		);
		return `Changed ${files} ${files === 1 ? "file" : "files"}`;
	}
	if (first.activityKind === "mcp_tool") {
		return `Used ${activities.length} ${activities.length === 1 ? "tool call" : "tool calls"}`;
	}
	if (first.activityKind === "auto_review") {
		return `Checked ${activities.length} ${activities.length === 1 ? "decision" : "decisions"}`;
	}
	if (first.activityKind === "plan") return "Updated plan";
	const category = commandCategory(first.detail?.command ?? first.summary);
	const verb = category === "read" || category === "search" ? "Explored" : "Ran";
	return `${verb} ${activities.length} ${activities.length === 1 ? "tool call" : "tool calls"}`;
}

type ActivityNode = { activity: ConversationActivity; children: ActivityNode[] };

function ActivityTree({ node }: { node: ActivityNode }) {
	return (
		<div className="flex flex-col">
			<ActivityRow activity={node.activity} />
			{node.children.length > 0 ? <NestedAgentRun nodes={node.children} /> : null}
		</div>
	);
}

function NestedAgentRun({ nodes }: { nodes: ActivityNode[] }) {
	const [open, setOpen] = useState(false);
	const reducedMotion = useReducedMotion();
	const count = countNodes(nodes);
	const running = nodes.some(nodeRunning);
	return (
		<div className="mb-2 overflow-hidden bg-background/40">
			<button
				type="button"
				onClick={() => setOpen((current) => !current)}
				aria-label={`Subagent ${count} ${count === 1 ? "step" : "steps"}`}
				aria-expanded={open}
				className="activity-row-toggle flex min-h-8 w-full select-none items-center gap-2 text-left text-[11px] text-muted-foreground outline-none hover:text-foreground focus-visible:outline-none"
			>
				<span className="font-medium text-foreground/80">Subagent</span>
				<span>{count} {count === 1 ? "step" : "steps"}</span>
				{running ? <Loader2 aria-hidden="true" className="size-3 animate-spin" /> : null}
				<ChevronRight aria-hidden="true" className={cn("size-3 transition-transform", open && "rotate-90")} />
			</button>
			<AnimatePresence initial={false}>
				{open ? (
					<motion.div
						initial={{ height: 0, opacity: 0 }}
						animate={{ height: "auto", opacity: 1 }}
						exit={{ height: 0, opacity: 0 }}
						transition={{ duration: reducedMotion ? 0 : 0.16, ease: [0.22, 1, 0.36, 1] }}
						className="border-t border-border/70"
					>
					{nodes.map((node) => <ActivityTree key={node.activity.id} node={node} />)}
					</motion.div>
				) : null}
			</AnimatePresence>
		</div>
	);
}

function buildHierarchy(activities: ConversationActivity[]): ActivityNode[] {
	const byProvider = new Map<string, ActivityNode>();
	const nodes = activities.map((activity) => {
		const node = { activity, children: [] } satisfies ActivityNode;
		if (activity.providerItemId) byProvider.set(activity.providerItemId, node);
		return node;
	});
	const roots: ActivityNode[] = [];
	for (const node of nodes) {
		const parentId = node.activity.detail?.parentProviderItemId;
		const parent = parentId ? byProvider.get(parentId) : undefined;
		if (parent && !wouldCreateCycle(node, parent, byProvider)) parent.children.push(node);
		else roots.push(node);
	}
	return roots;
}

function wouldCreateCycle(
	node: ActivityNode,
	parent: ActivityNode,
	byProvider: Map<string, ActivityNode>,
): boolean {
	const visited = new Set<ActivityNode>([node]);
	let current: ActivityNode | undefined = parent;
	while (current) {
		if (visited.has(current)) return true;
		visited.add(current);
		const parentId: string | undefined = current.activity.detail?.parentProviderItemId;
		current = parentId ? byProvider.get(parentId) : undefined;
	}
	return false;
}

function countNodes(nodes: ActivityNode[]): number {
	return nodes.reduce((count, node) => count + 1 + countNodes(node.children), 0);
}

function nodeRunning(node: ActivityNode): boolean {
	return node.activity.status === "running" || node.children.some(nodeRunning);
}

function summarize(activities: ConversationActivity[]): string {
	let toolCalls = 0;
	let changedFiles = 0;
	let exploratory = true;
	let hasPlan = false;

	for (const activity of activities) {
		if (activity.activityKind === "plan") {
			hasPlan = true;
			continue;
		}
		toolCalls += 1;
		const category = commandCategory(activity.detail?.command ?? activity.summary);
		if (activity.activityKind === "file_change") {
			changedFiles += Math.max(1, fileChangeFiles(activity).length);
			exploratory = false;
		} else if (
			activity.activityKind !== "command" ||
			(category !== "read" && category !== "search")
		) {
			exploratory = false;
		}
	}

	if (toolCalls === 0) return hasPlan ? "Updated plan" : `${activities.length} steps`;
	const label = `${exploratory ? "Explored" : "Ran"} ${toolCalls} ${toolCalls === 1 ? "tool call" : "tool calls"}`;
	const details = [
		changedFiles > 0
			? `${changedFiles} ${changedFiles === 1 ? "file changed" : "files changed"}`
			: undefined,
		hasPlan ? "updated plan" : undefined,
	].filter(Boolean);
	return details.length ? `${label} · ${details.join(" · ")}` : label;
}
