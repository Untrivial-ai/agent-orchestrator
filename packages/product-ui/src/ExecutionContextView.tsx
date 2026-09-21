export type ExecutionContextLabels = {
	active: string;
	baseBranch: string;
	branch: string;
	configured: string;
	executionContext: string;
	loading: string;
	orchestrator: string;
	path: string;
	repository: string;
	worker: string;
};

export type ExecutionContextViewProps = {
	activeAgent?: string;
	activeRole?: "worker" | "orchestrator";
	baseBranch?: string;
	branch?: string;
	error?: string;
	labels: ExecutionContextLabels;
	loading?: boolean;
	orchestratorAgent?: string;
	path?: string;
	projectName: string;
	repositories?: string[];
	workerAgent?: string;
};

/** Compact, read-only context shown before a task starts and in the inspector. */
export function ExecutionContextView({
	activeAgent,
	activeRole,
	baseBranch,
	branch,
	error,
	labels,
	loading = false,
	orchestratorAgent,
	path,
	projectName,
	repositories = [],
	workerAgent,
}: ExecutionContextViewProps) {
	const facts: Array<{ label: string; value: string; emphasis?: boolean }> = [];
	if (repositories.length > 0) facts.push({ label: labels.repository, value: repositories.join(", ") });
	if (branch) facts.push({ label: labels.branch, value: branch });
	if (baseBranch) facts.push({ label: labels.baseBranch, value: baseBranch });
	if (activeAgent) {
		facts.push({
			label: `${activeRole === "orchestrator" ? labels.orchestrator : labels.worker} (${labels.active})`,
			value: activeAgent,
			emphasis: true,
		});
	}
	if (workerAgent && workerAgent !== activeAgent) {
		facts.push({ label: `${labels.worker} (${labels.configured})`, value: workerAgent });
	}
	if (orchestratorAgent && orchestratorAgent !== activeAgent) {
		facts.push({ label: `${labels.orchestrator} (${labels.configured})`, value: orchestratorAgent });
	}

	return (
		<section
			aria-label={labels.executionContext}
			className="border-b border-border/70 bg-muted/20 px-4 py-3"
			data-testid="execution-context"
		>
			<div className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
				<span className="truncate" title={projectName}>{projectName}</span>
				<span className="shrink-0 text-passive">· {labels.executionContext}</span>
			</div>
			{loading ? (
				<p aria-busy="true" aria-label={labels.loading} className="mt-2 text-xs text-passive" role="status">
					{labels.loading}
				</p>
			) : null}
			{error ? <p className="mt-2 text-xs text-error" role="alert">{error}</p> : null}
			{!loading ? (
				<>
					{facts.length > 0 ? (
						<dl className="mt-2 grid min-w-0 gap-x-4 gap-y-1.5 text-xs sm:grid-cols-2">
							{facts.map((fact) => (
								<div className="min-w-0" key={`${fact.label}-${fact.value}`}>
									<dt className="text-passive">{fact.label}</dt>
									<dd className={fact.emphasis ? "truncate font-medium text-foreground" : "truncate text-foreground"} title={fact.value}>
										{fact.value}
									</dd>
								</div>
							))}
						</dl>
					) : null}
					{path ? (
						<div className="mt-2 flex min-w-0 items-center gap-1 text-2xs text-passive" title={path}>
							<span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-passive" />
							<span className="truncate">{labels.path}: {path}</span>
						</div>
					) : null}
				</>
			) : null}
		</section>
	);
}
