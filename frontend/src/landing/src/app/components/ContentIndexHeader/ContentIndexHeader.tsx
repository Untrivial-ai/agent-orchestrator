import type { ReactNode } from "react";
import { GridCross } from "@/app/blog/components/GridCross";

interface ContentIndexHeaderProps {
	eyebrow: string;
	title: string;
	description: ReactNode;
	children?: ReactNode;
}

export function ContentIndexHeader({
	eyebrow,
	title,
	description,
	children,
}: ContentIndexHeaderProps) {
	return (
		<header className="relative border-b border-border">
			<div className="max-w-3xl mx-auto px-6 pt-16 pb-10 md:pt-20 md:pb-12 relative">
				<GridCross className="top-0 left-0" />
				<GridCross className="top-0 right-0" />

				<span className="text-sm font-mono text-muted-foreground tracking-[0.5px]">
					{eyebrow}
				</span>
				<h1 className="text-3xl md:text-4xl font-medium tracking-[-0.5px] text-foreground mt-4">
					{title}
				</h1>
				<p className="text-muted-foreground mt-3 max-w-lg">{description}</p>
				{children}

				<GridCross className="bottom-0 left-0" />
				<GridCross className="bottom-0 right-0" />
			</div>
		</header>
	);
}
