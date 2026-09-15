import type { ReactNode } from "react";
import { cn } from "../../lib/utils";

/**
 * Figma section: column, gap 12px between heading and rows.
 * `sectionId` (optional) marks the section for the renderer smoke suite as
 * [data-testid="settings-section"][data-section=<id>].
 */
export function SettingsSection({
	title,
	sectionId,
	titleHidden,
	grouped,
	children,
}: {
	title: string;
	sectionId?: string;
	titleHidden?: boolean;
	grouped?: boolean;
	children: ReactNode;
}) {
	return (
		<section
			className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)"
			data-testid={sectionId ? "settings-section" : undefined}
			data-section={sectionId}
		>
			{!titleHidden && (
				<h2 className="text-xs font-medium leading-4 text-settings-muted">{title}</h2>
			)}
			<div
				className={cn(
					"w-full",
					grouped
						? "settings-grouped-rows flex w-full flex-col"
						: "flex w-full flex-col gap-1.5",
				)}
			>
				{children}
			</div>
		</section>
	);
}
