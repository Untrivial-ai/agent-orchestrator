import { Eraser, Import, MoreHorizontal, Pencil, Plus, Trash2, UserRound } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AoBridge } from "../../../preload";
import { aoBridge } from "../../lib/bridge";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { BrowserImportDialog } from "./BrowserImportDialog";

type ProfileBridge = AoBridge["browserProfiles"];
type Profile = Awaited<ReturnType<ProfileBridge["list"]>>["profiles"][number];
type DestructiveAction = { kind: "clear" | "delete"; profile: Profile };

export function BrowserProfilesSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const bridge = (aoBridge as Partial<AoBridge>).browserProfiles as ProfileBridge | undefined;
	const [profiles, setProfiles] = useState<Profile[]>([]);
	const [loading, setLoading] = useState(Boolean(bridge));
	const [error, setError] = useState("");
	const [name, setName] = useState("");
	const [renamingId, setRenamingId] = useState<string | null>(null);
	const [pendingAction, setPendingAction] = useState<DestructiveAction | null>(null);
	const [actionBusy, setActionBusy] = useState(false);
	const [actionError, setActionError] = useState("");
	const [importOpen, setImportOpen] = useState(false);

	const refresh = async () => {
		if (!bridge) return;
		setLoading(true);
		try {
			const result = await bridge.list();
			setProfiles(result.profiles);
			setError(result.error?.message ?? "");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : t("settings.browserProfiles.loadFailed"));
		} finally {
			setLoading(false);
		}
	};

	useEffect(() => {
		void refresh();
		// The bridge is fixed for the process lifetime; intentionally do not rerun
		// the load for every i18n/theme render.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [bridge]);

	const create = async () => {
		if (!bridge || !name.trim()) return;
		try {
			const created = await bridge.create(name);
			setProfiles((current) => [...current, created]);
			setName("");
			setError("");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : t("settings.browserProfiles.saveFailed"));
		}
	};

	const rename = async (profile: Profile, value: string) => {
		setRenamingId(null);
		const nextName = value.trim();
		if (!bridge || !nextName || nextName === profile.name) return;
		try {
			const updated = await bridge.rename({ id: profile.id, name: nextName });
			setProfiles((current) => current.map((entry) => (entry.id === profile.id ? updated : entry)));
			setError("");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : t("settings.browserProfiles.saveFailed"));
		}
	};

	const requestAction = (kind: DestructiveAction["kind"], profile: Profile) => {
		setActionError("");
		setPendingAction({ kind, profile });
	};

	const confirmAction = async () => {
		if (!bridge || !pendingAction) return;
		setActionBusy(true);
		setActionError("");
		try {
			if (pendingAction.kind === "clear") {
				await bridge.clear(pendingAction.profile.id);
			} else {
				await bridge.delete(pendingAction.profile.id);
				setProfiles((current) => current.filter((profile) => profile.id !== pendingAction.profile.id));
			}
			setError("");
			setPendingAction(null);
		} catch (reason) {
			setActionError(
				reason instanceof Error
					? reason.message
					: t(
							pendingAction.kind === "clear"
								? "settings.browserProfiles.clearFailed"
								: "settings.browserProfiles.deleteFailed",
						),
			);
		} finally {
			setActionBusy(false);
		}
	};

	return (
		<>
			<SettingsSection
				title={t("settings.browserProfiles")}
				sectionId="browserProfiles"
				titleHidden={titleHidden}
				grouped
			>
				<SettingsRow label={t("settings.browserImport.rowLabel")}>
					<Button disabled={!bridge} onClick={() => setImportOpen(true)} type="button" variant="secondary">
						<Import aria-hidden="true" className="size-icon-base" />
						{t("settings.browserImport.action")}
					</Button>
				</SettingsRow>
				<SettingsRow label={t("settings.browserProfiles.create")}>
					{/* One field with its submit embedded at the trailing edge. */}
					<form
						className="flex h-control-form w-52 min-w-0 items-center gap-1 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) pl-3 pr-1"
						onSubmit={(event) => {
							event.preventDefault();
							void create();
						}}
					>
						<input
							aria-label={t("settings.browserProfiles.name")}
							className="h-full min-w-0 flex-1 bg-transparent text-sm text-settings-label outline-none placeholder:text-settings-muted disabled:cursor-not-allowed disabled:opacity-50"
							disabled={!bridge || loading}
							maxLength={64}
							onChange={(event) => setName(event.target.value)}
							placeholder={t("settings.browserProfiles.namePlaceholder")}
							value={name}
						/>
						<Button
							aria-label={t("settings.browserProfiles.create")}
							className="size-control-sm text-settings-muted hover:text-settings-label"
							disabled={!name.trim() || !bridge || loading}
							size="none"
							type="submit"
							variant="ghost"
						>
							<Plus aria-hidden="true" className="size-icon-base" />
						</Button>
					</form>
				</SettingsRow>
			</SettingsSection>
			<SettingsSection title={t("settings.browserProfiles.profiles")}>
				{error ? <p className="text-xs text-destructive" role="alert">{error}</p> : null}
				{loading ? (
					<p className="py-2 text-xs text-settings-muted">{t("settings.browserProfiles.loading")}</p>
				) : profiles.length === 0 ? (
					<p className="py-2 text-xs text-settings-muted">{t("settings.browserProfiles.empty")}</p>
				) : (
					<ul className="flex flex-col divide-y divide-border">
						{profiles.map((profile) => (
							<BrowserProfileRow
								key={profile.id}
								profile={profile}
								renaming={renamingId === profile.id}
								onRenameStart={() => setRenamingId(profile.id)}
								onRenameEnd={(value) => void rename(profile, value)}
								onRenameCancel={() => setRenamingId(null)}
								onClear={() => requestAction("clear", profile)}
								onDelete={() => requestAction("delete", profile)}
							/>
						))}
					</ul>
				)}
				<p className="text-xs leading-relaxed text-settings-muted">{t("settings.browserProfiles.description")}</p>
			</SettingsSection>
			<ConfirmDialog
				open={pendingAction !== null}
				onOpenChange={(open) => {
					if (open || actionBusy) return;
					setPendingAction(null);
					setActionError("");
				}}
				title={pendingAction
					? t(
							pendingAction.kind === "clear"
								? "settings.browserProfiles.clearTitle"
								: "settings.browserProfiles.deleteTitle",
							{ profile: pendingAction.profile.name },
						)
					: ""}
				description={pendingAction
					? t(
							pendingAction.kind === "clear"
								? "settings.browserProfiles.clearDescription"
								: "settings.browserProfiles.deleteDescription",
						)
					: ""}
				confirmLabel={pendingAction?.kind === "clear"
					? t("settings.browserProfiles.clearConfirm")
					: t("settings.browserProfiles.deleteConfirm")}
				destructive
				busy={actionBusy}
				error={actionError || null}
				onConfirm={() => void confirmAction()}
			/>
			<BrowserImportDialog
				onImported={() => void refresh()}
				onOpenChange={setImportOpen}
				open={importOpen}
			/>
		</>
	);
}

/**
 * One named profile: its name, renamed in place, and a menu for the rest. The
 * menu keeps the row down to a single quiet control and names each action in
 * words, so clearing site data never hides behind an ambiguous icon.
 */
function BrowserProfileRow({
	profile,
	renaming,
	onRenameStart,
	onRenameEnd,
	onRenameCancel,
	onClear,
	onDelete,
}: {
	profile: Profile;
	renaming: boolean;
	onRenameStart: () => void;
	onRenameEnd: (value: string) => void;
	onRenameCancel: () => void;
	onClear: () => void;
	onDelete: () => void;
}) {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(profile.name);
	const inputRef = useRef<HTMLInputElement>(null);
	const triggerRef = useRef<HTMLButtonElement>(null);
	// The menu is still closing when Rename is chosen, and a modal menu pulls
	// focus back into itself until it has gone, so focus the field only once
	// the menu hands focus back.
	const focusFieldOnMenuClose = useRef(false);
	const cancelOnBlur = useRef(false);

	return (
		<li className="flex min-h-11 min-w-0 items-center gap-3 py-1.5">
			<UserRound aria-hidden="true" className="size-4 shrink-0 text-settings-muted" />
			{/* The name slot keeps the row's width whether it shows the name or the
			    field, so the menu button never moves while renaming. */}
			<div className="min-w-0 flex-1">
				{renaming ? (
					<input
						aria-label={t("settings.browserProfiles.renameInput", { profile: profile.name })}
						className="-ml-2 h-control-md w-full max-w-52 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-2 text-sm text-settings-label outline-none"
						data-settings-inline-edit=""
						maxLength={64}
						onBlur={() => {
							// Blur is the one place an edit ends: Enter and Escape move focus
							// to the row's menu button, so each path settles exactly once.
							if (cancelOnBlur.current) {
								cancelOnBlur.current = false;
								onRenameCancel();
							} else {
								onRenameEnd(draft);
							}
						}}
						onChange={(event) => setDraft(event.target.value)}
						onKeyDown={(event) => {
							if (event.key !== "Enter" && event.key !== "Escape") return;
							event.preventDefault();
							cancelOnBlur.current = event.key === "Escape";
							triggerRef.current?.focus();
						}}
						ref={inputRef}
						value={draft}
					/>
				) : (
					<span className="block truncate text-sm text-settings-label" title={profile.name}>
						{profile.name}
					</span>
				)}
			</div>
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<button
						aria-label={t("settings.browserProfiles.actions", { profile: profile.name })}
						className="settings-close-button shrink-0 focus-visible:bg-(--color-bg-settings-row-hover) focus-visible:text-settings-title data-[state=open]:bg-(--color-bg-settings-row-hover) data-[state=open]:text-settings-title"
						ref={triggerRef}
						type="button"
					>
						<MoreHorizontal aria-hidden="true" className="size-4" />
					</button>
				</DropdownMenuTrigger>
				<DropdownMenuContent
					align="end"
					className="min-w-40"
					onCloseAutoFocus={(event) => {
						if (!focusFieldOnMenuClose.current) return;
						focusFieldOnMenuClose.current = false;
						event.preventDefault();
						inputRef.current?.focus();
						inputRef.current?.select();
					}}
				>
					<DropdownMenuItem
						onSelect={() => {
							setDraft(profile.name);
							focusFieldOnMenuClose.current = true;
							onRenameStart();
						}}
					>
						<Pencil aria-hidden="true" />
						{t("shell.rename")}
					</DropdownMenuItem>
					<DropdownMenuItem onSelect={onClear}>
						<Eraser aria-hidden="true" />
						{t("settings.browserProfiles.clearConfirm")}
					</DropdownMenuItem>
					<DropdownMenuSeparator />
					<DropdownMenuItem
						className="text-destructive focus:text-destructive [&_svg]:text-destructive focus:[&_svg]:text-destructive"
						onSelect={onDelete}
					>
						<Trash2 aria-hidden="true" />
						{t("settings.browserProfiles.deleteConfirm")}
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
		</li>
	);
}
