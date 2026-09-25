import { ChevronLeft, ChevronRight, X } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { CloudCpSessionRepo } from "../lib/cloud-cp/types";
import { type CoderSize, useCoderSessionOptionsStore } from "../stores/coder-session-options-store";
import { useCoderTemplates } from "../hooks/useCoderTemplates";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { SearchablePicker } from "./SearchablePicker";

const SIZES: CoderSize[] = ["small", "medium", "large"];

// Project-level template and machine settings. Additional repositories render
// beside the primary repository picker, so they live in a separate component.
export function CoderTemplatePicker({ orgId }: { orgId: string | undefined }) {
	const { t } = useTranslation();
	const { templates } = useCoderTemplates(orgId, true);
	const templateId = useCoderSessionOptionsStore((s) => s.templateId);
	const supportedParams = useCoderSessionOptionsStore((s) => s.supportedParams);
	const size = useCoderSessionOptionsStore((s) => s.size);
	const startupScript = useCoderSessionOptionsStore((s) => s.startupScript);
	const setTemplate = useCoderSessionOptionsStore((s) => s.setTemplate);
	const setSize = useCoderSessionOptionsStore((s) => s.setSize);
	const setStartupScript = useCoderSessionOptionsStore((s) => s.setStartupScript);
	const reduceMotion = useReducedMotion();
	const [advancedOpen, setAdvancedOpen] = useState(startupScript.trim().length > 0);

	// Parameter controls appear only when the selected template declares them.
	const supportsSize = supportedParams.includes("size");
	const supportsStartup = supportedParams.includes("startup_script");
	const templateOptions = [
		{ id: "", name: t("coder.template.default", { defaultValue: "Default" }), description: t("coder.template.defaultHint", { defaultValue: "The workspace configured for your org." }), parameters: [] as string[] },
		...templates.map((tpl) => ({
			id: tpl.id,
			name: tpl.displayName || tpl.name,
			description: tpl.description,
			parameters: tpl.parameters ?? [],
		})),
	];

	return (
		<div className="flex flex-col gap-4 text-sm">
			<div className={`flex items-end transition-[gap] duration-300 motion-reduce:transition-none ${supportsSize ? "gap-3" : "gap-0"}`}>
				<div className="min-w-0 flex-1">
					<div className="flex flex-col gap-2">
						<span className="font-medium text-foreground">{t("coder.template.label", { defaultValue: "Template" })}</span>
						<SearchablePicker
							ariaLabel={t("coder.template.label", { defaultValue: "Template" })}
							placeholder={t("coder.template.default", { defaultValue: "Default" })}
							searchPlaceholder={t("coder.template.search", { defaultValue: "Search templates" })}
							value={templateId}
							onChange={(id) => {
								const selected = templateOptions.find((option) => option.id === id);
								if (selected) setTemplate(id, selected.parameters);
							}}
							options={templateOptions.map((option) => ({ value: option.id, label: option.name, description: option.description }))}
						/>
					</div>
				</div>
				<AnimatePresence initial={false}>
					{supportsSize ? (
						<motion.div
							key="machine-size"
							initial={reduceMotion ? false : { opacity: 0, width: 0, x: 12 }}
							animate={{ opacity: 1, width: "48%", x: 0 }}
							exit={reduceMotion ? undefined : { opacity: 0, width: 0, x: 12 }}
							transition={{ duration: reduceMotion ? 0 : 0.28, ease: "easeOut" }}
							className="min-w-0 overflow-hidden"
						>
							<div className="flex flex-col gap-2">
								<span className="whitespace-nowrap font-medium text-foreground">{t("coder.size.label", { defaultValue: "Machine size" })}</span>
								<Select value={size} onValueChange={(value) => setSize(value as CoderSize)}>
									<SelectTrigger className="w-full min-w-0">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{SIZES.map((value) => (
											<SelectItem key={value} value={value}>
												{t(`coder.size.${value}`, {
													defaultValue: value === "small" ? "Small · 2 vCPU / 8 GB" : value === "medium" ? "Medium · 4 vCPU / 16 GB" : "Large · 8 vCPU / 32 GB",
												})}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
						</motion.div>
					) : null}
				</AnimatePresence>
			</div>

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

// These cards are placed directly below the project's primary repository.
export function AdditionalRepositoriesPicker({ repos = [] }: { repos?: { label: string; url: string; private?: boolean }[] }) {
	const { t } = useTranslation();
	const extraRepos = useCoderSessionOptionsStore((s) => s.extraRepos);
	const setExtraRepos = useCoderSessionOptionsStore((s) => s.setExtraRepos);
	const reduceMotion = useReducedMotion();
	const [activeRepoIndex, setActiveRepoIndex] = useState(0);
	const previousRepoCount = useRef(extraRepos.length);
	useEffect(() => {
		if (extraRepos.length > previousRepoCount.current) setActiveRepoIndex(extraRepos.length - 1);
		if (extraRepos.length < previousRepoCount.current) setActiveRepoIndex((index) => Math.min(index, Math.max(0, extraRepos.length - 1)));
		previousRepoCount.current = extraRepos.length;
	}, [extraRepos.length]);

	const updateRepo = (index: number, patch: Partial<CloudCpSessionRepo>) => {
		setExtraRepos(extraRepos.map((repo, i) => (i === index ? { ...repo, ...patch } : repo)));
	};

	return (
		<AnimatePresence initial={false}>
			{extraRepos.length > 0 ? (
				<motion.div
					key="additional-repositories"
					initial={reduceMotion ? false : { opacity: 0, height: 0, y: -6 }}
					animate={{ opacity: 1, height: "auto", y: 0 }}
					exit={reduceMotion ? undefined : { opacity: 0, height: 0, y: -6 }}
					transition={{ duration: reduceMotion ? 0 : 0.25, ease: "easeOut" }}
					className="min-w-0 overflow-hidden"
				>
					<div className="space-y-2 pt-2">
						<span className="font-medium text-foreground">{t("coder.repos.label", { defaultValue: "Additional repositories" })}</span>
						<div className="overflow-hidden rounded-md" aria-label={t("coder.repos.label", { defaultValue: "Additional repositories" })}>
							<div className="flex transition-transform duration-300 ease-out motion-reduce:transition-none" style={{ transform: `translateX(-${activeRepoIndex * 100}%)` }}>
								{extraRepos.map((repo, index) => (
									// eslint-disable-next-line react/no-array-index-key
									<div key={index} className="flex w-full shrink-0 items-center gap-2 rounded-md border border-border bg-[var(--color-bg-import-card)] p-2">
										{repos.length > 0 ? (
											<SearchablePicker
												ariaLabel={`${t("coder.repos.url", { defaultValue: "Repository" })} ${index + 1}`}
												fixedScroll
												placeholder={t("coder.repos.select", { defaultValue: "Select a repository" })}
												searchPlaceholder={t("createProject.searchRepositories", { defaultValue: "Search repositories" })}
												value={repo.url}
												onChange={(url) => updateRepo(index, { url })}
												options={repos.map((option) => ({ value: option.url, label: option.label, private: option.private }))}
												className="min-w-0 flex-1"
											/>
										) : (
											<Input
												value={repo.url}
												onChange={(e) => updateRepo(index, { url: e.target.value })}
												placeholder={t("coder.repos.urlPlaceholder", { defaultValue: "https://github.com/owner/repo" })}
												className="min-w-0 flex-1"
												aria-label={t("coder.repos.url", { defaultValue: "Repository" })}
											/>
										)}
										<Input
											value={repo.branch ?? ""}
											onChange={(e) => updateRepo(index, { branch: e.target.value })}
											placeholder={t("coder.repos.branch", { defaultValue: "branch" })}
											className="w-32 shrink-0"
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
						</div>
						<AnimatePresence initial={false}>
							{extraRepos.length > 1 ? (
								<motion.div
									key="repository-controls"
									initial={reduceMotion ? false : { opacity: 0, height: 0, y: -6 }}
									animate={{ opacity: 1, height: "auto", y: 0 }}
									exit={reduceMotion ? undefined : { opacity: 0, height: 0, y: -6 }}
									transition={{ duration: reduceMotion ? 0 : 0.22, ease: "easeOut" }}
									className="overflow-hidden"
								>
									<div className="flex items-center justify-end gap-2 text-xs text-muted-foreground">
										<span aria-live="polite">{activeRepoIndex + 1} / {extraRepos.length}</span>
										<Button type="button" variant="ghost" aria-label={t("coder.repo.previous", { defaultValue: "Previous repository" })} disabled={activeRepoIndex === 0} onClick={() => setActiveRepoIndex((index) => index - 1)}><ChevronLeft className="size-4" aria-hidden="true" /></Button>
										<Button type="button" variant="ghost" aria-label={t("coder.repo.next", { defaultValue: "Next repository" })} disabled={activeRepoIndex === extraRepos.length - 1} onClick={() => setActiveRepoIndex((index) => index + 1)}><ChevronRight className="size-4" aria-hidden="true" /></Button>
									</div>
								</motion.div>
							) : null}
						</AnimatePresence>
					</div>
				</motion.div>
			) : null}
		</AnimatePresence>
	);
}
