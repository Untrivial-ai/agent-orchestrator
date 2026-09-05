import * as Dialog from "@radix-ui/react-dialog";
import { ChevronLeft, Folder, Link2, LoaderCircle, X } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../lib/bridge";
import { PathRow } from "./PathRow";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

export type CloneRepositoryDetails = {
	remoteUrl: string;
	destinationParent: string;
};

export type CloneRepositorySelection = CloneRepositoryDetails & {
	targetPath: string;
};

export const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";

export default function CloneRepositoryDialog({
	disabled,
	error,
	onBack,
	onChange,
	onClose,
	onContinue,
	onError,
	open,
	shake: externalShake = false,
	existingProjectPaths = [],
	existingProjectNames = [],
	value,
}: {
	disabled: boolean;
	error: string | null;
	onBack: () => void;
	onChange: (value: CloneRepositoryDetails) => void;
	onClose: () => void;
	onContinue: (selection: CloneRepositorySelection) => void;
	onError?: (message: string) => void;
	open: boolean;
	shake?: boolean;
	existingProjectPaths?: readonly string[];
	existingProjectNames?: readonly string[];
	value: CloneRepositoryDetails;
}) {
	const { t } = useTranslation();
	const [submitted, setSubmitted] = useState(false);
	const [repositoryCheck, setRepositoryCheck] = useState<"idle" | "checking" | "valid" | "invalid">("idle");
	const repositoryCheckRequest = useRef(0);
	const [choosingDestination, setChoosingDestination] = useState(false);
	const [shake, setShake] = useState(false);
	const [destinationPickerError, setDestinationPickerError] = useState<string | null>(null);
	const repositoryName = repositoryNameFromGitUrl(value.remoteUrl);
	const repositoryAvatar = repositoryAvatarFromGitUrl(value.remoteUrl);
	const hasRemoteUrl = value.remoteUrl.trim().length > 0;
	const targetPath = repositoryName && value.destinationParent
		? joinCloneDestination(value.destinationParent, repositoryName)
		: "";
	const projectExists = Boolean(
		repositoryName &&
		(existingProjectNames.some((name) => sameProjectName(name, repositoryName)) ||
			(targetPath && existingProjectPaths.some((path) => sameProjectPath(path, targetPath)))),
	);
	const urlError = hasRemoteUrl && !repositoryName ? t("createProject.cloneInvalidUrl") : null;
	const repositoryAccessError = repositoryName && repositoryCheck === "invalid"
		? t("createProject.cloneRepositoryUnavailable", {
				defaultValue: "This isn't a repository or you don't have access",
			})
		: null;
	const duplicateError = repositoryName && projectExists ? t("createProject.cloneProjectExists") : null;
	const inlineRepositoryError = urlError ?? repositoryAccessError ?? duplicateError;
	const destinationError = submitted && !value.destinationParent ? t("createProject.cloneDestinationRequired") : null;
	const canContinue = Boolean(
		repositoryName &&
		repositoryCheck === "valid" &&
		!projectExists &&
		!disabled &&
		!choosingDestination,
	);

	const triggerShake = () => {
		setShake(false);
		window.requestAnimationFrame(() => setShake(true));
		window.setTimeout(() => setShake(false), 320);
	};

	useEffect(() => {
		const requestId = ++repositoryCheckRequest.current;
		if (!repositoryName) {
			setRepositoryCheck("idle");
			return;
		}
		setRepositoryCheck("checking");
		const timer = window.setTimeout(() => {
			void aoBridge.app.checkGitRepository(value.remoteUrl.trim()).then((exists) => {
				if (requestId === repositoryCheckRequest.current) setRepositoryCheck(exists ? "valid" : "invalid");
			}).catch(() => {
				if (requestId === repositoryCheckRequest.current) setRepositoryCheck("invalid");
			});
		}, 300);
		return () => window.clearTimeout(timer);
	}, [repositoryName, value.remoteUrl]);

	const chooseDestination = async () => {
		setDestinationPickerError(null);
		setChoosingDestination(true);
		try {
			const selected = await aoBridge.app.chooseDirectory(t("createProject.cloneChooseDestination"));
			if (!selected) return;
			try {
				window.localStorage.setItem(LAST_CLONE_DESTINATION_KEY, selected);
			} catch {
				// Remembering the folder is a convenience; cloning still works if
				// browser storage is unavailable.
			}
			onChange({ ...value, destinationParent: selected });
		} catch (err) {
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			setDestinationPickerError(message);
			triggerShake();
			onError?.(message);
		} finally {
			setChoosingDestination(false);
		}
	};

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		setSubmitted(true);
		if (!canContinue) {
			if (!value.destinationParent) {
				const message = t("createProject.cloneDestinationRequired");
				triggerShake();
				onError?.(message);
			}
			return;
		}
		onContinue({
			...value,
			remoteUrl: value.remoteUrl.trim(),
			targetPath,
		});
	};

	return (
		<Dialog.Root open={open} onOpenChange={(next) => !next && !disabled && onClose()}>
			<Dialog.Portal>
				<Dialog.Content className={`fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none ${shake || externalShake ? "modal-shake" : ""}`}>
					<div className="relative flex shrink-0 items-center gap-3 px-4 pt-3">
						<Button
							type="button"
							variant="outline"
							size="icon"
							aria-label={t("createProject.cloneBack")}
							disabled={disabled || choosingDestination}
							onClick={onBack}
						>
							<ChevronLeft className="size-4" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1 pr-8">
							<Dialog.Title className="text-balance text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{t("createProject.cloneTitle")}
							</Dialog.Title>
							<Dialog.Description className="sr-only">
								{t("createProject.cloneDescription")}
							</Dialog.Description>
						</div>
						<button
							type="button"
							className="settings-close-button"
							aria-label={t("createProject.cloneClose")}
							disabled={disabled || choosingDestination}
							onClick={onClose}
						>
							<X className="size-4" aria-hidden="true" />
						</button>
					</div>

					<form className="min-h-0 overflow-y-auto" onSubmit={submit}>
						<div className="space-y-4 px-4 pb-1 pt-4">
							{error || destinationPickerError ? (
								<div className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-pretty text-[12px] leading-5 text-destructive" role="alert">
									{destinationPickerError ?? error}
								</div>
							) : null}

							<div className="space-y-2">
								<div className="relative">
									<Label htmlFor="cloneRepositoryUrl" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
										{t("createProject.cloneRepositoryUrl")}
									</Label>
									<AnimatePresence initial={false}>
										{inlineRepositoryError ? (
											<motion.p
												key={urlError ? "repository-url-error" : repositoryAccessError ? "repository-access-error" : "duplicate-project-error"}
												initial={{ opacity: 0, filter: "blur(2px)" }}
												animate={{ opacity: 1, filter: "blur(0px)" }}
												exit={{ opacity: 0, filter: "blur(2px)" }}
												transition={{ duration: 0.15, ease: "easeOut" }}
												className="absolute right-0 top-0 max-w-[65%] truncate overflow-hidden whitespace-nowrap text-right text-[12px] leading-5 text-destructive"
												role="alert"
											>
												{inlineRepositoryError}
											</motion.p>
										) : null}
									</AnimatePresence>
								</div>
								<div className="relative">
									<Input
										id="cloneRepositoryUrl"
										autoFocus
										autoCapitalize="none"
										autoComplete="off"
										aria-describedby="cloneRepositoryUrlHelp"
										aria-invalid={urlError || repositoryAccessError || duplicateError ? true : undefined}
										className="bg-[var(--color-bg-import-card)] pl-10 pr-10 font-mono text-[13px]"
										disabled={disabled}
										placeholder={t("createProject.cloneRepositoryUrlPlaceholder")}
										spellCheck={false}
										value={value.remoteUrl}
										onChange={(event) => onChange({ ...value, remoteUrl: event.target.value })}
									/>
									{repositoryCheck === "checking" ? (
										<LoaderCircle className="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 animate-spin text-muted-foreground" aria-label={t("createProject.cloneCheckingRepository", { defaultValue: "Checking repository" })} />
									) : null}
									<RepositoryOwnerIcon owner={repositoryAvatar?.owner ?? null} avatarUrl={repositoryAvatar?.url ?? null} />
								</div>
								<span id="cloneRepositoryUrlHelp" className="sr-only">
									{t("createProject.cloneRepositoryUrlHelp")}
								</span>
							</div>

							<div className="space-y-2">
								<Label htmlFor="cloneDestination" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
									{t("createProject.cloneDestination")}
								</Label>
								<PathRow
									action={t("createProject.cloneChoose")}
									ariaDescribedBy={destinationError ? "cloneDestinationError" : undefined}
									ariaInvalid={Boolean(destinationError)}
									ariaLabel={t("createProject.cloneChoose")}
									disabled={disabled || choosingDestination}
									icon={<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />}
									id="cloneDestination"
									onClick={() => void chooseDestination()}
								>
									{value.destinationParent || t("createProject.cloneDestinationPlaceholder")}
								</PathRow>
								{destinationError ? (
									<p id="cloneDestinationError" className="text-pretty text-[12px] leading-5 text-destructive" role="alert">
										{destinationError}
									</p>
								) : null}
							</div>

						</div>

						<div className="flex shrink-0 justify-end gap-2 px-4 pb-4 pt-3">
							<div className="flex items-center justify-end gap-3">
								<Button type="submit" variant="primary" disabled={!repositoryName || repositoryCheck !== "valid" || projectExists || disabled || choosingDestination}>
									{t("createProject.cloneContinue")}
								</Button>
							</div>
						</div>
					</form>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function RepositoryOwnerIcon({
	avatarUrl,
	owner,
}: {
	avatarUrl: string | null;
	owner: string | null;
}) {
	const [avatarState, setAvatarState] = useState<"loading" | "loaded" | "failed">("loading");
	const hasOwner = Boolean(owner);

	useEffect(() => {
		setAvatarState(avatarUrl ? "loading" : "failed");
	}, [avatarUrl]);

	const visible = hasOwner;
	const showAvatar = visible && avatarUrl && avatarState === "loaded";
	const showSkeleton = Boolean(visible && avatarUrl && avatarState === "loading");
	const showFallback = visible && (!avatarUrl || avatarState === "failed");

	return (
		<span className="pointer-events-none absolute left-3 top-1/2 z-10 flex size-4 -translate-y-1/2 items-center justify-center text-[var(--color-text-import-muted)]" aria-hidden="true">
			<span className="relative block size-4">
				{!visible ? <Link2 className="absolute inset-0 size-4" /> : null}
				{avatarUrl ? (
					<img
						alt=""
						className={`${showAvatar ? "opacity-100" : "opacity-0"} absolute inset-0 size-4 rounded-full object-cover outline outline-1 -outline-offset-1 outline-black/10 transition-none dark:outline-white/10`}
						draggable={false}
						loading="eager"
						onError={() => setAvatarState("failed")}
						onLoad={() => setAvatarState("loaded")}
						referrerPolicy="no-referrer"
						src={avatarUrl}
					/>
				) : null}
				{showSkeleton ? <span className="absolute inset-0 size-4 animate-pulse rounded-full bg-muted-foreground/40" /> : null}
				{showFallback ? <span className="absolute inset-0 size-4 rounded-full bg-muted text-center text-[9px] font-semibold leading-4 text-muted-foreground">{ownerInitials(owner)}</span> : null}
			</span>
		</span>
	);
}

function ownerInitials(owner: string | null): string {
	return owner?.split(/[-_\s/]+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase() ?? "").join("") || "?";
}

export function repositoryNameFromGitUrl(raw: string): string | null {
	const value = raw.trim();
	if (!value || /\s/.test(value) || value.startsWith("-")) return null;
	let remotePath = "";
	let host = "";
	const scpMatch = value.match(/^[^/@:\s]+@[^/:\s]+:(.+)$/);
	if (scpMatch?.[1]) {
		host = value.match(/^[^/@:\s]+@([^/:\s]+):/)?.[1]?.toLowerCase() ?? "";
		remotePath = scpMatch[1];
	} else {
		try {
			const parsed = new URL(value);
			if (!["file:", "git:", "http:", "https:", "ssh:"].includes(parsed.protocol)) return null;
			if (
				(["http:", "https:"].includes(parsed.protocol) &&
					(parsed.username || parsed.password || parsed.search)) ||
				parsed.password
			) {
				return null;
			}
			host = parsed.hostname.toLowerCase();
			// URL.pathname preserves percent escapes, while Go's net/url exposes a
			// decoded URL.Path to the daemon. Decode once so this preview names the
			// exact directory the daemon will create, including escaped separators.
			remotePath = decodeURIComponent(parsed.pathname);
		} catch {
			return null;
		}
	}
	const segments = remotePath.replace(/[\\/]+$/, "").split(/[\\/]/).filter(Boolean);
	if (segments.length < 2) return null;
	const providerSubpage = segments[2];
	if (
		(host === "github.com" &&
			["actions", "blob", "commit", "commits", "compare", "issues", "pull", "releases", "settings", "tree", "wiki"].includes(
				providerSubpage ?? "",
			)) ||
		(host === "bitbucket.org" && providerSubpage === "pull-requests") ||
		(host === "gitlab.com" && segments.includes("merge_requests"))
	) {
		return null;
	}
	const lastSegment = segments[segments.length - 1] ?? "";
	const name = lastSegment.replace(/\.git$/, "");
	if (!name || name === "." || name === ".." || /[\\/<>:"|?*]/.test(name)) return null;
	return name;
}

export function repositoryAvatarFromGitUrl(raw: string): { owner: string; url: string } | null {
	const remote = repositoryRemoteParts(raw);
	if (!remote) return null;
	const encodedOwner = encodeURIComponent(remote.owner);
	switch (remote.host) {
		case "github.com":
			return { owner: remote.owner, url: `https://github.com/${encodedOwner}.png?size=64` };
		case "gitlab.com":
			return { owner: remote.owner, url: `https://gitlab.com/-/avatar?username=${encodedOwner}` };
		case "bitbucket.org":
			return { owner: remote.owner, url: `https://bitbucket.org/account/${encodedOwner}/avatar/64/` };
		default:
			// Azure DevOps and self-hosted providers do not share one public avatar
			// endpoint. Unavatar knows the common provider URL shapes and the
			// initials fallback keeps this non-blocking when it cannot resolve one.
			return { owner: remote.owner, url: `https://unavatar.io/${encodeURIComponent(remote.host)}/${encodedOwner}` };
	}
}

type RepositoryRemoteParts = {
	host: string;
	owner: string;
};

function repositoryRemoteParts(raw: string): RepositoryRemoteParts | null {
	const value = raw.trim();
	if (!value || /\s/.test(value) || value.startsWith("-")) return null;

	let host = "";
	let remotePath = "";
	const scpMatch = value.match(/^[^/@:\s]+@([^/:\s]+):(.+)$/);
	if (scpMatch?.[1] && scpMatch[2]) {
		host = scpMatch[1].toLowerCase();
		remotePath = scpMatch[2];
	} else {
		try {
			const parsed = new URL(value);
			if (!["git:", "http:", "https:", "ssh:"].includes(parsed.protocol)) return null;
			if (parsed.username || parsed.password || parsed.search) return null;
			host = parsed.hostname.toLowerCase();
			remotePath = decodeURIComponent(parsed.pathname);
		} catch {
			return null;
		}
	}

	const segments = remotePath.replace(/[\\/]+$/, "").split(/[\\/]/).filter(Boolean);
	if (segments.length < 2) return null;
	const providerSubpage = segments[2];
	if (
		(host === "github.com" &&
			["actions", "blob", "commit", "commits", "compare", "issues", "pull", "releases", "settings", "tree", "wiki"].includes(
				providerSubpage ?? "",
			)) ||
		(host === "bitbucket.org" && providerSubpage === "pull-requests") ||
		(host === "gitlab.com" && segments.includes("merge_requests"))
	) {
		return null;
	}
	const repository = segments[segments.length - 1]?.replace(/\.git$/, "");
	const owner = segments[0];
	if (!repository || !owner || repository === "." || repository === ".." || /[\\/<>:"|?*]/.test(repository)) return null;
	return { host, owner };
}

export function joinCloneDestination(parent: string, repositoryName: string): string {
	const separator = parent.includes("\\") && !parent.includes("/") ? "\\" : "/";
	return `${parent.replace(/[\\/]+$/, "")}${separator}${repositoryName}`;
}

function sameProjectPath(left: string, right: string): boolean {
	const normalize = (path: string) => path.replace(/[\\/]+$/, "").replaceAll("\\", "/");
	const normalizedLeft = normalize(left);
	const normalizedRight = normalize(right);
	return normalizedLeft.toLowerCase() === normalizedRight.toLowerCase();
}

function sameProjectName(left: string, right: string): boolean {
	return left.trim().toLowerCase() === right.trim().toLowerCase();
}
