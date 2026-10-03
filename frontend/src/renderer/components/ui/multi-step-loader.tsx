import type { CSSProperties } from "react";
import { Check } from "lucide-react";
import { cn } from "../../lib/utils";

export function MultiStepLoader({
	ariaLabel,
	className,
	activeIndex,
	complete = false,
	duration = 380,
	steps,
}: {
	ariaLabel: string;
	className?: string;
	activeIndex: number;
	complete?: boolean;
	duration?: number;
	steps: readonly string[];
}) {
	if (steps.length === 0) return null;

	return (
		<div aria-label={ariaLabel} className={cn("w-80 max-w-[calc(100%-2rem)]", className)} role="status">
			<ol className="flex flex-col gap-3">
				{steps.map((step, index) => (
					<li aria-current={index === activeIndex ? "step" : undefined} className="flex min-h-7 items-center gap-3 text-sm leading-5" key={step}>
						<span aria-hidden="true" className="grid size-5 shrink-0 place-items-center">
							{index < activeIndex || (index === activeIndex && complete) ? (
								<Check className="size-4 text-[#60a5fa]" data-testid="multi-step-loader-check" strokeWidth={2} />
							) : (
								<span className={cn("size-2 rounded-full", index === activeIndex ? "multi-step-loader__dot bg-[#60a5fa]" : "bg-muted-foreground/40")} data-testid={index === activeIndex ? "multi-step-loader-active-dot" : undefined} />
							)}
						</span>
						{index === activeIndex && !complete ? (
							<span data-testid="multi-step-loader-step">
								<span
									className="multi-step-loader__step multi-step-loader__shimmer font-medium"
									key={activeIndex}
									style={{ "--multi-step-loader-duration": `${duration}ms` } as CSSProperties}
								>
									{step}
								</span>
							</span>
						) : <span className={index < activeIndex ? "text-foreground/80" : "text-muted-foreground/55"}>{step}</span>}
					</li>
				))}
			</ol>
		</div>
	);
}
