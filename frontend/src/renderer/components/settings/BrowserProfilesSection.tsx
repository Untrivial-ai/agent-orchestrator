import { Check, Eraser, Import, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AoBridge } from "../../../preload";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { BrowserImportDialog } from "./BrowserImportDialog";

type ProfileBridge = AoBridge["browserProfiles"];
type Profile = Awaited<ReturnType<ProfileBridge["list"]>>["profiles"][number];
type DestructiveAction = { kind: "clear" | "delete"; profile: Profile };

export function BrowserProfilesSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const bridge = (aoBridge as Partial<AoBridge>).browserProfiles as ProfileBridge | undefined;
	const [profiles, setProfiles] = useState<Awaited<ReturnType<ProfileBridge["list"]>>["profiles"]>([]);
	const [loading, setLoading] = useState(Boolean(bridge));
	const [error, setError] = useState("");
	const [name, setName] = useState("");
	const [editing, setEditing] = useState<Record<string, string>>({});
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

	const rename = async (id: string) => {
		if (!bridge) return;
		const nextName = editing[id]?.trim();
		if (!nextName) return;
		try {
			const updated = await bridge.rename({ id, name: nextName });
			setProfiles((current) => current.map((profile) => (profile.id === id ? updated : profile)));
			setEditing((current) => {
				const next = { ...current };
				delete next[id];
				return next;
			});
			setError("");
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : t("settings.browserProfiles.saveFailed"));
		}
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
			<p className="text-xs leading-relaxed text-muted-foreground">
				{t("settings.browserProfiles.description")}
			</p>
			<SettingsRow label={t("settings.browserImport.rowLabel")}>
				<Button disabled={!bridge} onClick={() => setImportOpen(true)} size="sm" type="button" variant="outline">
					<Import aria-hidden="true" className="size-icon-base" />
					{t("settings.browserImport.action")}
				</Button>
			</SettingsRow>
			<SettingsRow label={t("settings.browserProfiles.create")}>
				<form
					className="flex min-w-0 max-w-full items-center gap-1.5"
					onSubmit={(event) => {
						event.preventDefault();
						void create();
					}}
				>
					<Input
						aria-label={t("settings.browserProfiles.name")}
						className="h-control-md w-36"
						disabled={!bridge || loading}
						maxLength={64}
						onChange={(event) => setName(event.target.value)}
						placeholder={t("settings.browserProfiles.namePlaceholder")}
						value={name}
					/>
					<Button aria-label={t("settings.browserProfiles.create")} disabled={!name.trim() || !bridge || loading} size="icon-sm" type="submit" variant="outline">
						<Plus aria-hidden="true" className="size-icon-base" />
					</Button>
				</form>
			</SettingsRow>
			{loading ? (
				<p className="px-3 py-3 text-xs text-muted-foreground">{t("settings.browserProfiles.loading")}</p>
			) : profiles.length === 0 ? (
				<p className="px-3 py-3 text-xs text-muted-foreground">{t("settings.browserProfiles.empty")}</p>
			) : (
				profiles.map((profile) => (
					<SettingsRow key={profile.id} label={profile.name}>
						<div className="flex min-w-0 items-center gap-1.5">
							<Input
								aria-label={t("settings.browserProfiles.renameInput", { profile: profile.name })}
								className={cn("h-control-md w-36", !editing[profile.id] && "hidden")}
								maxLength={64}
								onChange={(event) => setEditing((current) => ({ ...current, [profile.id]: event.target.value }))}
								value={editing[profile.id] ?? profile.name}
							/>
							<Button
								aria-label={t("settings.browserProfiles.rename", { profile: profile.name })}
								onClick={() => {
									if (editing[profile.id] === undefined) setEditing((current) => ({ ...current, [profile.id]: profile.name }));
									else void rename(profile.id);
								}}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								{editing[profile.id] === undefined ? <Pencil aria-hidden="true" className="size-icon-base" /> : <Check aria-hidden="true" className="size-icon-base" />}
							</Button>
							<Button aria-label={t("settings.browserProfiles.clear", { profile: profile.name })} onClick={() => {
								setActionError("");
								setPendingAction({ kind: "clear", profile });
							}} size="icon-sm" type="button" variant="ghost">
								<Eraser aria-hidden="true" className="size-icon-base" />
							</Button>
							<Button aria-label={t("settings.browserProfiles.delete", { profile: profile.name })} onClick={() => {
								setActionError("");
								setPendingAction({ kind: "delete", profile });
							}} size="icon-sm" type="button" variant="ghost">
								<Trash2 aria-hidden="true" className="size-icon-base" />
							</Button>
						</div>
					</SettingsRow>
				))
			)}
			{error ? <p className="px-3 py-2 text-xs text-destructive" role="alert">{error}</p> : null}
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
