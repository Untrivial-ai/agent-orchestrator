import { GitBranch } from "lucide-react";

export function SessionBranchBadge({ branch }: { branch?: string }) {
	if (!branch?.trim()) return null;
	return (
		<span
			className="mr-1 inline-flex h-7 max-w-48 shrink-0 items-center gap-1.5 rounded-md border border-border bg-surface px-2 text-2xs text-muted-foreground"
			title={branch}
		>
			<GitBranch className="size-3.5 shrink-0" aria-hidden="true" />
			<span className="truncate font-mono">{branch}</span>
		</span>
	);
}
