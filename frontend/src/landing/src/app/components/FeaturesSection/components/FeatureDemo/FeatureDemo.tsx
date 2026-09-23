import type { ReactNode } from "react";

interface FeatureDemoProps {
	children: ReactNode;
	/** Base name under /optimized, e.g. "feature3" (no width or extension). */
	backgroundImage: string;
	className?: string;
}

const WIDTHS = [640, 960, 1280] as const;
const SIZES = "(max-width: 640px) 100vw, (max-width: 1279px) 92vw, 50vw";

const srcSet = (base: string, ext: "avif" | "webp") =>
	WIDTHS.map((w) => `/optimized/${base}-${w}.${ext} ${w}w`).join(", ");

export function FeatureDemo({
	children,
	backgroundImage,
	className = "",
}: FeatureDemoProps) {
	return (
		<div
			className={`relative w-full overflow-hidden sm:min-h-[300px] lg:aspect-4/3 ${className}`}
		>
			{/*
				Every feature row sits below the fold, so the artwork is lazy: React
				hoists a <link rel=preload as=image> into <head> for any non-lazy <img>
				it renders on the server, which previously put four full-width
				backgrounds ahead of the hero in the priority queue.
			*/}
			<picture>
				<source
					type="image/avif"
					srcSet={srcSet(backgroundImage, "avif")}
					sizes={SIZES}
				/>
				<img
					src={`/optimized/${backgroundImage}-1280.webp`}
					srcSet={srcSet(backgroundImage, "webp")}
					sizes={SIZES}
					alt=""
					loading="lazy"
					decoding="async"
					draggable={false}
					className="pointer-events-none absolute inset-0 h-full w-full select-none object-cover"
				/>
			</picture>
			<div className="absolute inset-0 bg-background/35" />

			{/* Content overlay */}
			<div className="relative z-10 flex h-full w-full items-center justify-start p-4 sm:justify-center sm:p-6">
				{children}
			</div>
		</div>
	);
}
