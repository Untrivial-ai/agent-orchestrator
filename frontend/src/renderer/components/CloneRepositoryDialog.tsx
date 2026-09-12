import * as Dialog from "@radix-ui/react-dialog";
import { ChevronLeft, Folder, Link2, LoaderCircle, X } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../lib/bridge";
import { isMacPlatform, isWindowsPlatform } from "../lib/platform";
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
	const destinationPickerRequest = useRef(0);
	const shakeFrame = useRef<number | null>(null);
	const shakeTimer = useRef<number | null>(null);
	const lastReportedRepositoryError = useRef<string | null>(null);
	const [choosingDestination, setChoosingDestination] = useState(false);
	const [repositoryFieldTouched, setRepositoryFieldTouched] = useState(false);
	const [shake, setShake] = useState(false);
	const [destinationPickerError, setDestinationPickerError] = useState<string | null>(null);
	const repositoryName = repositoryNameFromGitUrl(value.remoteUrl);
	const repositoryAvatar = repositoryAvatarFromGitUrl(value.remoteUrl);
	const hasRemoteUrl = value.remoteUrl.trim().length > 0;
	const hasDestination = value.destinationParent.trim().length > 0;
	const targetPath = repositoryName && hasDestination
		? joinCloneDestination(value.destinationParent.trim(), repositoryName)
		: "";
	const projectExists = Boolean(
		targetPath && existingProjectPaths.some((path) => sameProjectPath(path, targetPath)),
	);
	const urlError = hasRemoteUrl && !repositoryName ? t("createProject.cloneInvalidUrl") : null;
	const repositoryAccessError = repositoryName && repositoryCheck === "invalid"
		? t("createProject.cloneRepositoryUnavailable", {
				defaultValue: "This isn't a repository or you don't have access",
			})
		: null;
	const duplicateError = repositoryName && projectExists ? t("createProject.cloneProjectExists") : null;
	const deferredRepositoryError = urlError ?? duplicateError;
	const inlineRepositoryError = repositoryAccessError ?? (
		repositoryFieldTouched || submitted ? deferredRepositoryError : null
	);
	const parentError = error === inlineRepositoryError ? null : error;
	const destinationError = submitted && !hasDestination ? t("createProject.cloneDestinationRequired") : null;
	const canContinue = Boolean(
		repositoryName &&
		repositoryCheck === "valid" &&
		hasDestination &&
		!projectExists &&
		!disabled &&
		!choosingDestination,
	);

	const clearShake = useCallback(() => {
		if (shakeFrame.current !== null) window.cancelAnimationFrame(shakeFrame.current);
		if (shakeTimer.current !== null) window.clearTimeout(shakeTimer.current);
		shakeFrame.current = null;
		shakeTimer.current = null;
		setShake(false);
	}, []);

	const triggerShake = useCallback(() => {
		clearShake();
		setShake(false);
		shakeFrame.current = window.requestAnimationFrame(() => {
			setShake(true);
			shakeFrame.current = null;
		});
		shakeTimer.current = window.setTimeout(() => {
			setShake(false);
			shakeTimer.current = null;
		}, 320);
	}, [clearShake]);

	useEffect(() => {
		if (open) return;
		repositoryCheckRequest.current += 1;
		destinationPickerRequest.current += 1;
		setSubmitted(false);
		setRepositoryCheck("idle");
		setChoosingDestination(false);
		setRepositoryFieldTouched(false);
		setDestinationPickerError(null);
		lastReportedRepositoryError.current = null;
		clearShake();
	}, [clearShake, open]);

	useEffect(() => {
		if (!open || !inlineRepositoryError) {
			lastReportedRepositoryError.current = null;
			return;
		}
		if (lastReportedRepositoryError.current === inlineRepositoryError) return;
		lastReportedRepositoryError.current = inlineRepositoryError;
		triggerShake();
		onError?.(inlineRepositoryError);
	}, [inlineRepositoryError, onError, open, triggerShake]);

	useEffect(() => () => {
		repositoryCheckRequest.current += 1;
		destinationPickerRequest.current += 1;
		if (shakeFrame.current !== null) window.cancelAnimationFrame(shakeFrame.current);
		if (shakeTimer.current !== null) window.clearTimeout(shakeTimer.current);
	}, []);

	useEffect(() => {
		const requestId = ++repositoryCheckRequest.current;
		if (!open || !repositoryName) {
			setRepositoryCheck("idle");
			return;
		}
		setRepositoryCheck("checking");
		const timer = window.setTimeout(() => {
			void aoBridge.app.checkGitRepository(value.remoteUrl.trim()).then((exists) => {
				if (requestId !== repositoryCheckRequest.current) return;
				setRepositoryCheck(exists ? "valid" : "invalid");
			}).catch(() => {
				if (requestId !== repositoryCheckRequest.current) return;
				setRepositoryCheck("invalid");
			});
		}, 300);
		return () => window.clearTimeout(timer);
	}, [open, repositoryName, value.remoteUrl]);

	const chooseDestination = async () => {
		const requestId = ++destinationPickerRequest.current;
		setDestinationPickerError(null);
		setChoosingDestination(true);
		try {
			const selected = await aoBridge.app.chooseDirectory({
				title: t("createProject.cloneChooseDestination"),
				defaultPath: "~/ao/projects",
			});
			if (requestId !== destinationPickerRequest.current) return;
			if (!selected) return;
			try {
				window.localStorage.setItem(LAST_CLONE_DESTINATION_KEY, selected);
			} catch {
				// Remembering the folder is a convenience; cloning still works if
				// browser storage is unavailable.
			}
			onChange({ ...value, destinationParent: selected });
		} catch (err) {
			if (requestId !== destinationPickerRequest.current) return;
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			setDestinationPickerError(message);
			triggerShake();
			onError?.(message);
		} finally {
			if (requestId === destinationPickerRequest.current) setChoosingDestination(false);
		}
	};

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		setSubmitted(true);
		if (!canContinue) {
			if (!hasDestination) {
				const message = t("createProject.cloneDestinationRequired");
				triggerShake();
				onError?.(message);
			}
			return;
		}
		try {
			window.localStorage.setItem(LAST_CLONE_DESTINATION_KEY, value.destinationParent.trim());
		} catch {
			// Remembering the folder is optional.
		}
		onContinue({
			...value,
			remoteUrl: value.remoteUrl.trim(),
			destinationParent: value.destinationParent.trim(),
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
							{parentError || destinationPickerError ? (
								<div className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-pretty text-[12px] leading-5 text-destructive" role="alert">
									{destinationPickerError ?? parentError}
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
												id="cloneRepositoryUrlError"
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
										aria-describedby={`cloneRepositoryUrlHelp${inlineRepositoryError ? " cloneRepositoryUrlError" : ""}`}
										aria-invalid={urlError || repositoryAccessError || duplicateError ? true : undefined}
										className="bg-[var(--color-bg-import-card)] pl-10 pr-10 font-mono text-[13px]"
										disabled={disabled}
										placeholder={t("createProject.cloneRepositoryUrlPlaceholder")}
										spellCheck={false}
										value={value.remoteUrl}
										onChange={(event) => onChange({ ...value, remoteUrl: event.target.value })}
										onBlur={() => setRepositoryFieldTouched(true)}
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
								<div className="flex h-control-form items-center overflow-hidden rounded-md border border-transparent bg-[var(--color-bg-import-card)] text-[13px] text-foreground">
									<div className="relative min-w-0 flex-1">
										<Folder className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--color-text-import-muted)]" aria-hidden="true" />
										<Input
											id="cloneDestination"
											className="border-transparent bg-transparent pl-10 font-mono text-[13px]"
											aria-describedby={`cloneDestinationHelp${destinationError ? " cloneDestinationError" : ""}`}
											aria-invalid={Boolean(destinationError)}
											disabled={disabled || choosingDestination}
											autoCapitalize="none"
											autoComplete="off"
											spellCheck={false}
											placeholder={t("createProject.cloneDestinationPlaceholder")}
											value={value.destinationParent}
											onChange={(event) => onChange({ ...value, destinationParent: event.target.value })}
										/>
									</div>
									<button
										aria-label={t("createProject.cloneChooseDestination")}
										className="flex h-full shrink-0 items-center border-l border-border/60 px-4 text-foreground outline-none transition-colors hover:bg-foreground/10 focus-visible:bg-foreground/10 disabled:pointer-events-none disabled:opacity-50"
										disabled={disabled || choosingDestination}
										type="button"
										onClick={() => void chooseDestination()}
									>
										{t("createProject.cloneChoose")}
									</button>
								</div>
								<p id="cloneDestinationHelp" className="text-pretty text-[12px] leading-5 text-[var(--color-text-import-muted)]">
									{targetPath
										? t("createProject.cloneDestinationTarget", { path: targetPath })
										: t("createProject.cloneDestinationHelp")}
								</p>
								{destinationError ? (
									<p id="cloneDestinationError" className="text-pretty text-[12px] leading-5 text-destructive" role="alert">
										{destinationError}
									</p>
								) : null}
							</div>

						</div>

						<div className="flex shrink-0 justify-end gap-2 px-4 pb-4 pt-3">
							<div className="flex items-center justify-end gap-3">
								<Button type="submit" variant="primary" disabled={!canContinue}>
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
	// The backend derives the checkout name from the final path segment. A
	// self-hosted remote can validly use a single segment, for example
	// https://git.example.com/repository.git.
	if (segments.length < 1) return null;
	if (isProviderSubpage(host, segments)) return null;
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
	if (isProviderSubpage(host, segments)) return null;
	const repository = segments[segments.length - 1]?.replace(/\.git$/, "");
	const owner = segments[0];
	if (!repository || !owner || repository === "." || repository === ".." || /[\\/<>:"|?*]/.test(repository)) return null;
	return { host, owner };
}

function isProviderSubpage(host: string, segments: string[]): boolean {
	const subpage = segments[2] ?? "";
	return (host === "github.com" && ["actions", "blob", "commit", "commits", "compare", "issues", "pull", "releases", "settings", "tree", "wiki"].includes(subpage)) ||
		(host === "bitbucket.org" && subpage === "pull-requests") ||
		(host === "gitlab.com" && segments.includes("merge_requests"));
}

export function joinCloneDestination(parent: string, repositoryName: string): string {
	const separator = parent.includes("\\") && !parent.includes("/") ? "\\" : "/";
	return `${parent.replace(/[\\/]+$/, "")}${separator}${repositoryName}`;
}

export function sameProjectPath(
	left: string,
	right: string,
	caseInsensitive = isMacPlatform() || isWindowsPlatform(),
): boolean {
	const normalize = (path: string) => path.replace(/[\\/]+$/, "").replaceAll("\\", "/");
	const normalizedLeft = normalize(left);
	const normalizedRight = normalize(right);
	return caseInsensitive
		? normalizedLeft.toLowerCase() === normalizedRight.toLowerCase()
		: normalizedLeft === normalizedRight;
}
