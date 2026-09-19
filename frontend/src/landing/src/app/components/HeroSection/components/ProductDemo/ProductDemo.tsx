"use client";

import { AppMockup } from "../AppMockup";

// The LCP element. Single-format srcset on purpose: with a <picture> the
// preload scanner would fetch the <img> candidate while the element resolves to
// an <source> candidate, downloading the hero twice.
const HERO_WIDTHS = [720, 1080, 1440] as const;
const HERO_SRCSET = HERO_WIDTHS.map(
	(w) => `/optimized/hero-background-${w}.webp ${w}w`,
).join(", ");

export function ProductDemo() {
	return (
		<div className="relative w-full max-w-full">
			<div className="relative aspect-[1140/700] overflow-hidden bg-card p-2 shadow-[0_40px_120px_-50px_rgba(0,0,0,0.9)] sm:aspect-auto sm:min-h-[560px] sm:p-4 lg:min-h-[720px] lg:p-6">
				<img
					src="/optimized/hero-background-1440.webp"
					srcSet={HERO_SRCSET}
					sizes="(max-width: 1536px) 100vw, 1536px"
					alt=""
					fetchPriority="high"
					decoding="async"
					draggable={false}
					className="pointer-events-none absolute inset-0 h-full w-full select-none object-cover"
				/>
				<div className="pointer-events-none absolute inset-0 bg-background/15" />
				<div className="pointer-events-none absolute inset-x-8 top-8 h-24 rounded-full bg-foreground/[0.16] blur-3xl" />
				<div className="pointer-events-none absolute inset-[10%] top-[20%] rounded-3xl bg-white/[0.12] blur-[60px]" />
				<AppMockup />
			</div>
		</div>
	);
}
