import { Check } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AgentSwitchPresentation } from "../lib/agent-switch-presentation";
import { agentLabel } from "../lib/agent-options";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./AgentAvatar";

export const agentSwitchHandoffSteps = [
	{ key: "preparing", labelKey: "switchAgent.state.preparingHandoff" },
	{ key: "stopping_source", labelKey: "switchAgent.state.stoppingSource" },
	{ key: "starting_target", labelKey: "switchAgent.state.startingTarget" },
	{ key: "confirming_takeover", labelKey: "switchAgent.state.deliveringContext" },
] as const;

export type AgentSwitchHandoffStepKey = (typeof agentSwitchHandoffSteps)[number]["key"];

type Props = {
	fromHarness: string;
	targetHarness: string;
	stage: AgentSwitchPresentation["stage"];
	variant?: "full" | "compact";
};

// The arc must span the gap between the two agent tiles. Stretching a fixed
// viewBox with preserveAspectRatio="none" distorted the curve and turned the orb
// into an ellipse, so the SVG instead keeps the viewBox aspect ratio (via
// `aspect-[…]` in the markup) and scales uniformly with the available width.
const geometry = {
	full: { viewBox: "0 0 200 56", d: "M 6 46 C 60 46, 60 10, 100 10 S 140 46, 194 46", aspect: "aspect-[25/7]" },
	compact: { viewBox: "0 0 200 36", d: "M 6 30 C 60 30, 60 6, 100 6 S 140 30, 194 30", aspect: "aspect-[50/9]" },
} as const;

function stepIndex(stage: AgentSwitchPresentation["stage"]): number {
	const i = agentSwitchHandoffSteps.findIndex((s) => s.key === stage);
	return i < 0 ? 0 : i;
}

export function AgentSwitchHandoffVisual({ fromHarness, targetHarness, stage, variant = "full" }: Props) {
	const { t } = useTranslation();
	const active = stepIndex(stage);
	const compact = variant === "compact";
	const pathId = useId().replace(/[^a-zA-Z0-9_-]/g, "handoff");
	const arc = geometry[compact ? "compact" : "full"];
	// Toast state machine: a single stage is visible at a time. When the daemon
	// advances `stage`, the current orb resolves to a green tick for a beat, then
	// the next stage's label eases in with a soft fade.
	const [displayed, setDisplayed] = useState(active);
	const [resolved, setResolved] = useState(false);
	useEffect(() => {
		if (displayed === active) {
			setResolved(false);
			return;
		}
		setResolved(true);
		const timer = window.setTimeout(() => {
			setDisplayed(active);
			setResolved(false);
		}, 480);
		return () => window.clearTimeout(timer);
	}, [active, displayed]);
	const shown = agentSwitchHandoffSteps[displayed];
	return (
		<div className="flex w-full flex-col items-center gap-1" data-testid="agent-switch-handoff-visual" data-stage={stage ?? "unknown"} data-active-step={shown.key}>
			<div className={cn("flex w-full items-center justify-center", compact ? "gap-2" : "gap-1 sm:gap-2")}>
				<HandoffAgentMark harness={fromHarness} compact={compact} />
				<svg aria-hidden="true" className={cn("agent-switch-handoff-curve min-w-0 max-w-64 flex-1", arc.aspect)} data-testid="agent-switch-handoff-curve" viewBox={arc.viewBox}>
					<path id={pathId} d={arc.d} fill="none" strokeWidth="1.5" strokeDasharray="1 6" strokeLinecap="round" className="agent-switch-handoff-track" />
					<path d={arc.d} fill="none" strokeWidth="1.5" strokeDasharray="1 6" strokeLinecap="round" className="agent-switch-handoff-flow" />
					<circle r="6" className="agent-switch-handoff-orb-halo"><animateMotion dur="2.2s" repeatCount="indefinite"><mpath href={`#${pathId}`} /></animateMotion></circle>
					<circle r="4" className="agent-switch-handoff-orb"><animateMotion dur="2.2s" repeatCount="indefinite"><mpath href={`#${pathId}`} /></animateMotion></circle>
				</svg>
				<HandoffAgentMark harness={targetHarness} highlight compact={compact} />
			</div>
			{/* Screen-reader live region announces each stage as it changes; the
			    visible toast shows that same single stage. */}
			<p className="sr-only" data-testid="agent-switch-handoff-active-label" aria-live="polite" key={shown.key}>
				{t(shown.labelKey)}
			</p>
			<div className="agent-switch-handoff-toast" data-testid="agent-switch-handoff-toast" data-phase={resolved ? "resolved" : "working"} data-step={shown.key}>
				<span aria-hidden="true" className={cn("agent-switch-handoff-toast-orb", compact && "size-4")}>
					{resolved ? <Check aria-hidden="true" className="size-3" strokeWidth={3} /> : <span aria-hidden="true" className="agent-switch-handoff-orb-core" />}
				</span>
				<span key={shown.key} data-testid="agent-switch-handoff-toast-label" className={cn("agent-switch-handoff-toast-label", compact && "text-[11px] leading-4")}>
					{t(shown.labelKey)}
				</span>
			</div>
		</div>
	);
}

function HandoffAgentMark({ harness, highlight = false, compact = false }: { harness: string; highlight?: boolean; compact?: boolean }) {
	return (
		<div className={cn("flex shrink-0 flex-col items-center", compact ? "min-w-10 gap-1" : "min-w-20 gap-2")}>
			<span className={cn("grid place-items-center rounded-xl border bg-surface/90 shadow-lg shadow-black/20", compact ? "size-10" : "size-14", highlight ? "border-accent/60" : "border-border-strong")}>
				<AgentAvatar className={compact ? "size-6" : "size-8"} decorative provider={harness} />
			</span>
			{compact ? null : <span className="text-caption font-medium text-muted-foreground">{agentLabel(harness)}</span>}
		</div>
	);
}
