"use client";

import { FeatureDemo } from "./components/FeatureDemo";
import { DelegationDemo } from "./components/DelegationDemo/DelegationDemo";
import { FeedbackLoopDemo } from "./components/FeedbackLoopDemo/FeedbackLoopDemo";
import { FleetBoardDemo } from "./components/FleetBoardDemo/FleetBoardDemo";
import { MobileAppDemo } from "./components/MobileAppDemo/MobileAppDemo";
import { ProjectAgentsDemo } from "./components/ProjectAgentsDemo/ProjectAgentsDemo";
import { FEATURES } from "./constants";

const DEMO_COMPONENTS = [
	DelegationDemo,
	FleetBoardDemo,
	FeedbackLoopDemo,
	ProjectAgentsDemo,
	MobileAppDemo,
];

const FEATURE_BACKGROUNDS = [
	"/optimized/feature3.webp",
	"/optimized/feature.webp",
	"/optimized/feature4.webp",
	"/optimized/feature2.webp",
	"/optimized/feature3.webp",
] as const;

export function FeaturesSection() {
	return (
		<section
			id="features"
			className="relative px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px] lg:py-24"
		>
			<div className="max-w-7xl mx-auto">
				{/* Feature Rows */}
				<div className="space-y-20 sm:space-y-24 lg:space-y-32">
					{FEATURES.map((feature, index) => {
						const isReversed = index % 2 === 1;
						const DemoComponent = DEMO_COMPONENTS[index];
						return (
							<div
								key={feature.title}
								className={`grid grid-cols-1 items-center gap-8 sm:gap-10 xl:grid-cols-2 xl:gap-16 ${
									isReversed ? "xl:direction-rtl" : ""
								}`}
							>
								{/* Text Content */}
								<div
									className={`space-y-6 ${isReversed ? "xl:order-2 xl:text-right" : "xl:order-1"}`}
								>
									<div className="space-y-4">
										<span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
											{feature.tag}
										</span>
										<h3 className="text-balance text-2xl sm:text-3xl lg:text-4xl font-medium tracking-[-0.5px] text-foreground">
											{feature.title}
										</h3>
									</div>
									<p
										className={`max-w-[500px] text-pretty text-base sm:text-lg text-muted-foreground leading-relaxed ${isReversed ? "xl:ml-auto" : ""}`}
									>
										{feature.description}
									</p>
								</div>

								{/* Demo */}
								<div className={`${isReversed ? "xl:order-1" : "xl:order-2"}`}>
									<FeatureDemo
										backgroundImage={
											FEATURE_BACKGROUNDS[index % FEATURE_BACKGROUNDS.length]
										}
										preload
									>
										{DemoComponent && <DemoComponent />}
									</FeatureDemo>
								</div>
							</div>
						);
					})}
				</div>
			</div>
		</section>
	);
}
