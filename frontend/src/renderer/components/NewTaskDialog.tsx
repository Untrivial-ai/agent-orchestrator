import * as Dialog from "@radix-ui/react-dialog";
import { useQueries } from "@tanstack/react-query";
import { StickyNote } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useUiStore } from "../stores/ui-store";
import { useTranslation } from "react-i18next";
import { useRemoteWorkspaces, useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { labelForHost } from "../lib/host-clients";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { TaskComposer } from "./TaskComposer";
import { CLOUD_PROJECT_KIND, STANDALONE_WORKSPACE_ID, STANDALONE_PROJECT_KIND } from "../types/workspace";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";

function repositoryOwnerAvatar(repo: string): { owner: string; url: string } | null {
	const value = repo.trim();
	const scp = value.match(/^[^/@:\s]+@([^/:\s]+):(.+)$/);
	let host: string;
	let pathname: string;
	if (scp?.[1] && scp[2]) {
		host = scp[1].toLowerCase();
		pathname = scp[2];
	} else {
		try {
			const parsed = new URL(value.includes("://") ? value : `https://${value}`);
			host = parsed.hostname.toLowerCase();
			pathname = parsed.pathname;
		} catch {
			return null;
		}
	}
	const owner = pathname.replace(/^\/+|\/+$/g, "").split("/")[0]?.replace(/\.git$/, "");
	if (!owner) return null;
	const encodedOwner = encodeURIComponent(owner);
	if (host === "github.com") return { owner, url: `https://github.com/${encodedOwner}.png?size=64` };
	if (host === "gitlab.com") return { owner, url: `https://gitlab.com/-/avatar?username=${encodedOwner}` };
	if (host === "bitbucket.org") return { owner, url: `https://bitbucket.org/account/${encodedOwner}/avatar/64/` };
	return { owner, url: `https://unavatar.io/${encodeURIComponent(host)}/${encodedOwner}` };
}

function ProjectOwnerAvatar({ avatar, className }: { avatar: { owner: string; url: string }; className: string }) {
	return (
		<span className={`relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-full border border-card bg-muted font-medium text-muted-foreground ${className}`} aria-hidden="true">
			<span className="text-[0.65em] leading-none">{avatar.owner.charAt(0).toUpperCase()}</span>
			<img
				key={avatar.url}
				width={18}
				height={18}
				className="absolute inset-0 size-full rounded-full object-cover"
				src={avatar.url}
				alt=""
				onError={(event) => { event.currentTarget.style.visibility = "hidden"; }}
			/>
		</span>
	);
}

type NewTaskDialogProps = {
	open: boolean;
	projectId?: string;
	hostId?: string;
	onProjectChange?: (projectId: string) => void;
	onCreated: (sessionId: string) => void;
	onOpenChange: (open: boolean) => void;
};

export function NewTaskDialog({ open, projectId, hostId, onProjectChange, onCreated, onOpenChange }: NewTaskDialogProps) {
	const { t } = useTranslation();
	const [accountSlot, setAccountSlot] = useState<HTMLElement | null>(null);
	// Settings opens as a page in the center pane; step aside so it is not hidden behind this modal.
	const settingsOpen = useUiStore((state) => state.settingsModal?.scope === "global");
	const localWorkspaces = useWorkspaceQuery({ subscribed: open }).data ?? [];
	const remoteWorkspaces = useRemoteWorkspaces({ subscribed: open }).data ?? [];
	const workspaces = hostId ? remoteWorkspaces.filter((workspace) => workspace.hostId === hostId) : localWorkspaces;
	const projects = useMemo(() => workspaces.filter(
		(workspace) => workspace.id !== STANDALONE_WORKSPACE_ID && workspace.kind !== STANDALONE_PROJECT_KIND,
	), [workspaces]);
	const localProjects = useMemo(() => hostId ? [] : projects.filter((project) => project.kind !== CLOUD_PROJECT_KIND), [hostId, projects]);
	const localProjectDetails = useQueries({
		queries: localProjects.map((project) => ({
			queryKey: ["project", project.id],
			enabled: open,
			staleTime: 30_000,
			queryFn: async () => {
				const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
					params: { path: { id: project.id } },
				});
				if (error) throw new Error(apiErrorMessage(error));
				if (data?.status !== "ok") return undefined;
				return data.project;
			},
		})),
	});
	const projectAvatars = useMemo(() => new Map(projects.map((project) => {
		const seen = new Set<string>();
		const detailsIndex = localProjects.findIndex((localProject) => localProject.id === project.id);
		const details = detailsIndex >= 0 ? localProjectDetails[detailsIndex]?.data : undefined;
		const primaryRepo = details && "repo" in details && typeof details.repo === "string" ? details.repo : "";
		const workspaceRepos = details && "workspaceRepos" in details ? details.workspaceRepos ?? [] : project.workspaceRepos ?? [];
		const repositories = [
			...(primaryRepo ? [{ repo: primaryRepo }] : []),
			...workspaceRepos,
		];
		const avatars = repositories.flatMap(({ repo }) => {
			const avatar = repositoryOwnerAvatar(repo);
			if (!avatar) return [];
			const identity = `${new URL(avatar.url).hostname}/${avatar.owner.toLowerCase()}`;
			if (seen.has(identity)) return [];
			seen.add(identity);
			return [avatar];
		});
		return [project.id, avatars] as const;
	})), [localProjectDetails, localProjects, projects]);
	const selectedProjectId = projectId ?? STANDALONE_WORKSPACE_ID;
	const selectedProject = projects.find((project) => project.id === selectedProjectId);
	const selectedProjectName = selectedProject?.name ?? t("standalone.workspaceName");
	const selectedProjectAvatar = projectAvatars.get(selectedProjectId)?.[0];
	const projectOptions = [
		{ value: STANDALONE_WORKSPACE_ID, label: t("standalone.workspaceName") },
		...projects.map((project) => ({ value: project.id, label: project.name })),
	];
	useEffect(() => {
		if (!open) return;
		for (const avatars of projectAvatars.values()) {
			for (const avatar of avatars) {
				const image = new Image();
				image.src = avatar.url;
			}
		}
	}, [open, projectAvatars]);
	return (
		<Dialog.Root open={open && !settingsOpen} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-dialog-xl -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					{/* The selected project is the dialog title; the composer remains the main surface. */}
					<Dialog.Title className="settings-dialog-title flex flex-wrap items-center gap-x-1.5 px-3 pr-11 pt-3">
						<SettingsOptionMenu
							aria-label={t("newTask.project")}
							value={selectedProjectId}
							options={projectOptions}
							onChange={(value) => onProjectChange?.(value)}
							menuAlign="start"
							menuClassName="w-48 max-h-72"
							triggerClassName="w-fit max-w-[45vw] min-w-0 gap-1 rounded-md bg-transparent! px-1! py-0! text-[length:inherit]! font-semibold leading-[inherit]! text-foreground hover:bg-interactive-hover! hover:text-foreground data-[state=open]:bg-transparent! data-[state=open]:hover:bg-interactive-hover! data-[state=open]:text-foreground"
							menuItemClassName="gap-1!"
							renderTrigger={() => (
								<span className="inline-flex min-w-0 items-center gap-1.5">
									{selectedProjectId === STANDALONE_WORKSPACE_ID ? (
										<StickyNote aria-hidden="true" className="size-[0.85em] shrink-0 -translate-y-px text-muted-foreground" />
									) : selectedProjectAvatar ? (
										<ProjectOwnerAvatar avatar={selectedProjectAvatar} className="size-[1em]" />
									) : <StickyNote aria-hidden="true" className="size-[0.85em] shrink-0 -translate-y-px text-muted-foreground" />}
									<span className="min-w-0 -translate-y-px truncate">{selectedProjectName}</span>
								</span>
							)}
							renderMenuItem={(option) => {
								const avatars = projectAvatars.get(option.value) ?? [];
								return (
									<>
										<span className="relative flex w-5 shrink-0 items-center justify-center">
											{option.value === STANDALONE_WORKSPACE_ID ? (
												<StickyNote aria-hidden="true" className="size-4! -translate-y-px text-muted-foreground" />
											) : (
												<span className="flex items-center -space-x-2">
													{avatars.slice(0, 2).map((avatar) => (
														<ProjectOwnerAvatar key={`${avatar.owner}:${avatar.url}`} avatar={avatar} className="size-[18px]" />
													))}
												</span>
											)}
											{avatars.length > 2 ? <span className="absolute -bottom-1 -right-1 flex size-3 items-center justify-center rounded-full border border-card bg-muted text-[8px] text-muted-foreground">+{avatars.length - 2}</span> : null}
										</span>
										<span className="min-w-0 truncate text-settings-label">{option.label}</span>
									</>
								);
							}}
						/>
						{hostId ? <span className="text-settings-muted">· {labelForHost(hostId) ?? hostId}</span> : null}
					</Dialog.Title>
					<div ref={setAccountSlot} className="absolute right-2.5 top-2.5" />
					<Dialog.Description className="sr-only">
						{t(selectedProjectId === STANDALONE_WORKSPACE_ID ? "newTask.standaloneDescription" : "newTask.description")}
					</Dialog.Description>
					<TaskComposer
						projectId={selectedProjectId}
						hostId={hostId}
						createLabel={t("newTask.create")}
						autoFocusTitle
						accountControlContainer={accountSlot}
						onCreated={(sessionId) => {
							onCreated(sessionId);
							onOpenChange(false);
						}}
					/>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
