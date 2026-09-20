import { Check } from "lucide-react";
import { motion, useReducedMotion } from "motion/react";
import { useEffect, useRef, useState, type CSSProperties } from "react";
import { cn } from "../../lib/utils";

export function MultiStepLoader({
	ariaLabel,
	className,
	duration = 3_500,
	steps,
}: {
	ariaLabel: string;
	className?: string;
	duration?: number;
	steps: readonly string[];
}) {
	const [activeIndex, setActiveIndex] = useState(0);
	const reduceMotion = useReducedMotion();
	const stepRef = useRef<HTMLSpanElement>(null);

	useEffect(() => {
		const step = stepRef.current;
		if (!step) return;
		const advance = () => setActiveIndex((current) => (current + 1) % steps.length);
		step.addEventListener("animationend", advance);
		return () => step.removeEventListener("animationend", advance);
	}, [activeIndex, steps.length]);

	if (steps.length === 0) return null;

	return (
		<p
			aria-label={ariaLabel}
			className={cn("inline-flex min-h-7 items-center justify-center", className)}
			role="status"
		>
			<span
				className="multi-step-loader__step inline-flex items-center gap-3"
				data-testid="multi-step-loader-step"
				key={activeIndex}
				ref={stepRef}
				style={{ "--multi-step-loader-duration": `${duration}ms` } as CSSProperties}
			>
				<span className="relative grid size-5 shrink-0 place-items-center" aria-hidden="true">
					<span className="multi-step-loader__dot size-2 rounded-full bg-[#60a5fa] shadow-[0_0_8px_rgba(96,165,250,0.38)]" />
					<span className="multi-step-loader__check absolute inset-0 grid place-items-center rounded-full bg-[#60a5fa]/12 text-[#60a5fa]">
						<Check className="size-3" strokeWidth={2.25} />
					</span>
				</span>
				<motion.span
					animate={reduceMotion ? undefined : { backgroundPosition: ["180% center", "-80% center"] }}
					className="bg-[linear-gradient(100deg,var(--color-text-muted)_15%,#93c5fd_48%,var(--color-text-muted)_82%)] bg-[length:220%_100%] bg-clip-text text-sm font-medium leading-5 text-transparent"
					transition={{ duration: 2.8, ease: "linear", repeat: Infinity }}
				>
					{steps[activeIndex]}
				</motion.span>
			</span>
		</p>
	);
}
