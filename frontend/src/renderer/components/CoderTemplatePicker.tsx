import { Plus, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import type { CloudCpSessionRepo } from "../lib/cloud-cp/types";
import { type CoderSize, useCoderSessionOptionsStore } from "../stores/coder-session-options-store";
import { useCoderTemplates } from "../hooks/useCoderTemplates";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

const SIZES: CoderSize[] = ["small", "medium", "large"];

// The coder dev-kit picker shown at project setup. Kept deliberately compact: a
// row of template tiles (Default + whatever the deployment offers), an inline
// additional-repositories list, and — only once a non-default template is
// chosen — a size selector and an Advanced disclosure for a startup script.
// "Default" with no extra repos sends nothing, so the project behaves exactly as
// it does today. `repos` are the org's GitHub repositories, used to offer a
// picker for each extra repo (mirroring the primary repository selector); when
// the list is unavailable it falls back to a free-text URL field.
export function CoderTemplatePicker({
	orgId,
	repos = [],
}: {
	orgId: string | undefined;
	repos?: { label: string; url: string }[];
}) {
	const { t } = useTranslation();
	const { templates } = useCoderTemplates(orgId, true);
	const templateId = useCoderSessionOptionsStore((s) => s.templateId);
	const supportedParams = useCoderSessionOptionsStore((s) => s.supportedParams);
	const size = useCoderSessionOptionsStore((s) => s.size);
	const startupScript = useCoderSessionOptionsStore((s) => s.startupScript);
	const extraRepos = useCoderSessionOptionsStore((s) => s.extraRepos);
	const setTemplate = useCoderSessionOptionsStore((s) => s.setTemplate);
	const setSize = useCoderSessionOptionsStore((s) => s.setSize);
	const setStartupScript = useCoderSessionOptionsStore((s) => s.setStartupScript);
	const setExtraRepos = useCoderSessionOptionsStore((s) => s.setExtraRepos);

	const [advancedOpen, setAdvancedOpen] = useState(startupScript.trim().length > 0);

	// A control is offered only when the chosen template declares its parameter,
	// so a paramless template shows no form and can never send a value Coder
	// would reject.
	const supportsSize = supportedParams.includes("size");
	const supportsStartup = supportedParams.includes("startup_script");

	const tiles = [
		{ id: "", name: t("coder.template.default", { defaultValue: "Default" }), description: t("coder.template.defaultHint", { defaultValue: "The workspace configured for your org." }), parameters: [] as string[] },
		...templates.map((tpl) => ({
			id: tpl.id,
			name: tpl.displayName || tpl.name,
			description: tpl.description,
			parameters: tpl.parameters ?? [],
		})),
	];

	const updateRepo = (index: number, patch: Partial<CloudCpSessionRepo>) => {
		setExtraRepos(extraRepos.map((repo, i) => (i === index ? { ...repo, ...patch } : repo)));
	};

	return (
		<div className="flex flex-col gap-4 text-sm">
			{/* Template tiles */}
			<div className="flex flex-col gap-2">
				<span className="font-medium text-foreground">{t("coder.template.label", { defaultValue: "Template" })}</span>
				<div className="flex flex-wrap gap-2">
					{tiles.map((tile) => {
						const selected = templateId === tile.id;
						return (
							<button
								key={tile.id || "__default__"}
								type="button"
								aria-pressed={selected}
								onClick={() => setTemplate(tile.id, tile.parameters)}
								className={cn(
									"flex min-w-40 max-w-64 flex-col items-start gap-0.5 rounded-md border px-3 py-2 text-left transition-colors",
									selected
										? "border-primary bg-primary/5 ring-1 ring-primary"
										: "border-border hover:border-foreground/30 hover:bg-muted/50",
								)}
							>
								<span className="text-sm font-medium text-foreground">{tile.name}</span>
								{tile.description ? (
									<span className="line-clamp-1 text-xs text-muted-foreground">{tile.description}</span>
								) : null}
							</button>
						);
					})}
				</div>
			</div>

			{/* Repositories: primary repo comes from the project; these clone alongside it. */}
			<div className="flex flex-col gap-2">
				<span className="font-medium text-foreground">
					{t("coder.repos.label", { defaultValue: "Additional repositories" })}
				</span>
				{extraRepos.length === 0 ? (
					<span className="text-xs text-muted-foreground">
						{t("coder.repos.hint", { defaultValue: "Clone extra repos beside the project's primary repo." })}
					</span>
				) : (
					<div className="flex flex-col gap-2">
						{extraRepos.map((repo, index) => (
							// eslint-disable-next-line react/no-array-index-key
							<div key={index} className="flex items-center gap-2">
								{repos.length > 0 ? (
									<Select value={repo.url || undefined} onValueChange={(url) => updateRepo(index, { url })}>
										<SelectTrigger
											className="flex-1"
											aria-label={t("coder.repos.url", { defaultValue: "Repository" })}
										>
											<SelectValue placeholder={t("coder.repos.select", { defaultValue: "Select a repository" })} />
										</SelectTrigger>
										<SelectContent>
											{repos.map((option) => (
												<SelectItem key={option.url} value={option.url}>
													{option.label}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								) : (
									<Input
										value={repo.url}
										onChange={(e) => updateRepo(index, { url: e.target.value })}
										placeholder={t("coder.repos.urlPlaceholder", { defaultValue: "https://github.com/owner/repo" })}
										className="flex-1"
										aria-label={t("coder.repos.url", { defaultValue: "Repository" })}
									/>
								)}
								<Input
									value={repo.branch ?? ""}
									onChange={(e) => updateRepo(index, { branch: e.target.value })}
									placeholder={t("coder.repos.branch", { defaultValue: "branch" })}
									className="w-32"
									aria-label={t("coder.repos.branch", { defaultValue: "branch" })}
								/>
								<Button
									type="button"
									variant="ghost"
									aria-label={t("coder.repos.remove", { defaultValue: "Remove repository" })}
									onClick={() => setExtraRepos(extraRepos.filter((_, i) => i !== index))}
								>
									<X aria-hidden="true" className="size-icon-sm" />
								</Button>
							</div>
						))}
					</div>
				)}
				<div>
					<Button type="button" variant="outline" onClick={() => setExtraRepos([...extraRepos, { url: "", branch: "" }])}>
						<Plus aria-hidden="true" className="size-icon-sm" />
						{t("coder.repos.add", { defaultValue: "Add repository" })}
					</Button>
				</div>
			</div>

			{/* Size + startup only appear when the chosen template declares them. */}
			{supportsSize ? (
				<div className="flex flex-col gap-2">
					<span className="font-medium text-foreground">{t("coder.size.label", { defaultValue: "Machine size" })}</span>
					<Select value={size} onValueChange={(value) => setSize(value as CoderSize)}>
						<SelectTrigger className="max-w-56">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{SIZES.map((value) => (
								<SelectItem key={value} value={value}>
									{t(`coder.size.${value}`, {
										defaultValue:
											value === "small" ? "Small · 2 vCPU / 8 GB" : value === "medium" ? "Medium · 4 vCPU / 16 GB" : "Large · 8 vCPU / 32 GB",
									})}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</div>
			) : null}

			{supportsStartup ? (
				<div className="flex flex-col gap-2">
					<button
						type="button"
						className="w-fit text-xs font-medium text-muted-foreground hover:text-foreground"
						onClick={() => setAdvancedOpen(!advancedOpen)}
					>
						{advancedOpen
							? t("coder.startup.hide", { defaultValue: "Hide startup script" })
							: t("coder.startup.show", { defaultValue: "Add startup script" })}
					</button>
					{advancedOpen ? (
						<textarea
							value={startupScript}
							onChange={(e) => setStartupScript(e.target.value)}
							rows={4}
							spellCheck={false}
							placeholder={t("coder.startup.placeholder", { defaultValue: "# runs once the workspace is ready\nmake dev" })}
							className="w-full rounded-md border border-border bg-transparent px-3 py-2 font-mono text-xs text-foreground outline-none focus-visible:ring-1 focus-visible:ring-primary"
						/>
					) : null}
				</div>
			) : null}
		</div>
	);
}
