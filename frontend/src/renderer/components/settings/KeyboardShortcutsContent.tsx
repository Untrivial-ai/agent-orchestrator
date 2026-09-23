import { Check, Keyboard, Pencil, Plus, RotateCcw, Search, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { shortcutCategoryLabelKeys, shortcutLabelKeys } from "../../i18n/key-maps";
import { useEffect, useMemo, useRef, useState } from "react";
import {
	APP_SHORTCUTS,
	effectiveShortcutBindings,
	matchesShortcutBinding,
	shortcutBindingLabel,
	shortcutBindingValidationError,
	type AppShortcutId,
	type ShortcutCategory,
	type KeybindingOverrides,
	type ShortcutBinding,
} from "../../../shared/shortcuts";
import { isMacPlatform } from "../../lib/platform";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { useKeybindingsStore } from "../../stores/keybindings-store";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { ConfirmDialog } from "../ConfirmDialog";

function shortcutLabel(id: AppShortcutId, t: TFunction): string {
	return t(shortcutLabelKeys[id]);
}

function shortcutCategoryLabel(category: ShortcutCategory, t: TFunction): string {
	return t(shortcutCategoryLabelKeys[category]);
}

type RecordingState = { id: AppShortcutId; mode: "replace" | "add" };
type ConflictState = {
	targetId: AppShortcutId;
	conflictingId: AppShortcutId;
	binding: ShortcutBinding;
	mode: RecordingState["mode"];
};
type ToastState = {
	title: string;
	body?: string;
	undo?: () => Promise<void>;
};

const ignoredRecordingKeys = new Set([
	"Alt",
	"AltGraph",
	"CapsLock",
	"Control",
	"Dead",
	"Meta",
	"NumLock",
	"Process",
	"ScrollLock",
	"Shift",
	"Unidentified",
]);

function eventBinding(event: KeyboardEvent): ShortcutBinding | null {
	if (ignoredRecordingKeys.has(event.key) || event.getModifierState?.("AltGraph")) return null;
	return {
		key: event.key.length === 1 ? event.key.toLowerCase() : event.key,
		...(event.code === "Backquote" ? { code: event.code } : {}),
		ctrl: event.ctrlKey,
		meta: event.metaKey,
		shift: event.shiftKey,
		alt: event.altKey,
	};
}

function definition(id: AppShortcutId) {
	return APP_SHORTCUTS.find((shortcut) => shortcut.id === id);
}

export function KeyboardShortcutsContent({
	active,
	isMac = isMacPlatform(),
}: {
	active: boolean;
	isMac?: boolean;
}) {
	const { t } = useTranslation();
	const overrides = useKeybindingsStore((state) => state.overrides);
	const setOverrides = useKeybindingsStore((state) => state.setOverrides);
	const resetBinding = useKeybindingsStore((state) => state.resetBinding);
	const resetAll = useKeybindingsStore((state) => state.resetAll);
	const [query, setQuery] = useState("");
	const [recording, setRecordingState] = useState<RecordingState | null>(null);
	const [conflict, setConflict] = useState<ConflictState | null>(null);
	const [confirmResetAll, setConfirmResetAll] = useState(false);
	const [toast, setToast] = useState<ToastState | null>(null);
	const toastTimerRef = useRef<number | undefined>(undefined);
	const activeRef = useRef(active);
	const recordingRequestRef = useRef(0);
	activeRef.current = active;

	const showToast = (next: ToastState) => {
		if (toastTimerRef.current !== undefined) window.clearTimeout(toastTimerRef.current);
		setToast(next);
		toastTimerRef.current = window.setTimeout(() => setToast(null), 3500);
	};

	useEffect(
		() => () => {
			activeRef.current = false;
			recordingRequestRef.current += 1;
			if (toastTimerRef.current !== undefined) window.clearTimeout(toastTimerRef.current);
			void aoBridge.keybindings.setRecording(false);
		},
		[],
	);

	useEffect(() => {
		if (active) return;
		setRecordingState(null);
		void aoBridge.keybindings.setRecording(false);
	}, [active]);

	useEffect(() => {
		if (!recording) return;
		const handleBlur = () => {
			setRecordingState(null);
			void aoBridge.keybindings.setRecording(false);
		};
		window.addEventListener("blur", handleBlur);
		return () => window.removeEventListener("blur", handleBlur);
	}, [recording]);

	const beginRecording = async (next: RecordingState) => {
		const request = recordingRequestRef.current + 1;
		recordingRequestRef.current = request;
		try {
			await aoBridge.keybindings.setRecording(true);
			if (!activeRef.current || request !== recordingRequestRef.current) {
				await aoBridge.keybindings.setRecording(false);
				return;
			}
			setRecordingState(next);
		} catch {
			showToast({
				title: t("shortcut.couldNotRecord"),
				body: t("shortcut.tryReopen"),
			});
		}
	};

	const endRecording = () => {
		recordingRequestRef.current += 1;
		setRecordingState(null);
		void aoBridge.keybindings.setRecording(false);
	};

	const filteredShortcuts = useMemo(() => {
		const needle = query.trim().toLowerCase();
		if (!needle) return APP_SHORTCUTS;
		return APP_SHORTCUTS.filter((shortcut) => {
			const labels = effectiveShortcutBindings(shortcut.id, isMac, overrides)
				.map((candidate) => shortcutBindingLabel(candidate, isMac))
				.join(" ");
			return `${shortcutLabel(shortcut.id, t)} ${shortcutCategoryLabel(shortcut.category, t)} ${labels}`.toLowerCase().includes(needle);
		});
	}, [isMac, overrides, query, t]);

	const applyBinding = async (
		targetId: AppShortcutId,
		candidate: ShortcutBinding,
		mode: RecordingState["mode"],
		conflictingId?: AppShortcutId,
	) => {
		const before = overrides;
		const next: KeybindingOverrides = { ...overrides };
		const currentTarget = effectiveShortcutBindings(targetId, isMac, overrides);
		next[targetId] = mode === "add" ? [...currentTarget, candidate].slice(-2) : [candidate];
		if (conflictingId) {
			next[conflictingId] = effectiveShortcutBindings(conflictingId, isMac, overrides).filter(
				(existing) => !matchesShortcutBinding(candidate, existing),
			);
		}
		await setOverrides(next);
		const targetLabel = shortcutLabel(targetId, t);
		const conflictLabel = conflictingId ? shortcutLabel(conflictingId, t) : undefined;
		showToast({
			title: conflictingId ? t("shortcut.reassigned") : t("shortcut.updated"),
			body: conflictingId
				? t("shortcut.movedBody", {
						binding: shortcutBindingLabel(candidate, isMac),
						from: conflictLabel ?? "",
						to: targetLabel,
					})
				: t("shortcut.updatedBody", { name: targetLabel, binding: shortcutBindingLabel(candidate, isMac) }),
			undo: async () => {
				await setOverrides(before);
				showToast({ title: t("shortcut.undone") });
			},
		});
	};

	useEffect(() => {
		if (!active || !recording) return;
		const handleKeyDown = (event: KeyboardEvent) => {
			event.preventDefault();
			event.stopPropagation();
			if (event.key === "Escape") {
				endRecording();
				return;
			}
			if (event.repeat) return;
			const candidate = eventBinding(event);
			if (!candidate) {
				return;
			}
			const validationError = shortcutBindingValidationError(candidate, isMac);
			if (validationError) {
				showToast({ title: t("shortcut.reserved"), body: validationError });
				return;
			}
			if (
				recording.mode === "add" &&
				effectiveShortcutBindings(recording.id, isMac, overrides).some((existing) =>
					matchesShortcutBinding(candidate, existing),
				)
			) {
				showToast({
					title: t("shortcut.alreadyAssigned"),
					body: t("shortcut.alreadyAvailable", { binding: shortcutBindingLabel(candidate, isMac) }),
				});
				endRecording();
				return;
			}
			const conflicting = APP_SHORTCUTS.find(
				(shortcut) =>
					shortcut.id !== recording.id &&
					effectiveShortcutBindings(shortcut.id, isMac, overrides).some((existing) =>
						matchesShortcutBinding(candidate, existing),
					),
			);
			if (conflicting) {
				if (conflicting.customizable === false) {
					showToast({
						title: t("shortcut.reserved"),
						body: t("shortcut.usedByFixed", { binding: shortcutBindingLabel(candidate, isMac), name: shortcutLabel(conflicting.id, t) }),
					});
					return;
				}
				setConflict({
					targetId: recording.id,
					conflictingId: conflicting.id,
					binding: candidate,
					mode: recording.mode,
				});
				endRecording();
				return;
			}
			void applyBinding(recording.id, candidate, recording.mode).catch(() =>
				showToast({ title: t("shortcut.couldNotUpdate"), body: t("shortcut.previousStillActive") }),
			);
			endRecording();
		};
		window.addEventListener("keydown", handleKeyDown, true);
		return () => window.removeEventListener("keydown", handleKeyDown, true);
	}, [isMac, active, overrides, recording]);

	const handleResetBinding = async (id: AppShortcutId) => {
		const before = overrides;
		await resetBinding(id);
		showToast({
			title: t("shortcut.restored"),
			body: `${definition(id)?.label ?? id} now uses its default binding.`,
			undo: async () => {
				await setOverrides(before);
				showToast({ title: t("shortcut.undone") });
			},
		});
	};

	const handleRemoveBinding = async (id: AppShortcutId, index: number) => {
		const before = overrides;
		const current = effectiveShortcutBindings(id, isMac, overrides);
		const removed = current[index];
		await setOverrides({ ...overrides, [id]: current.filter((_, candidateIndex) => candidateIndex !== index) });
		showToast({
			title: t("shortcut.removed"),
			body: removed ? `${definition(id)?.label ?? id} no longer uses ${shortcutBindingLabel(removed, isMac)}.` : undefined,
			undo: async () => {
				await setOverrides(before);
				showToast({ title: t("shortcut.undone") });
			},
		});
	};

	return (
		<>
			<div className="relative flex flex-col gap-3">
				<div className="sticky top-0 z-10 flex flex-col gap-3 bg-[var(--color-bg-settings-row)] pb-2">
					<label className="flex h-9! min-w-0 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3">
						<Search
							className="size-4 shrink-0 text-settings-muted"
							aria-hidden="true"
						/>
						<Input
							type="search"
							className="h-auto min-w-0 flex-1 border-0 bg-transparent p-0 text-sm text-settings-label outline-none ring-0 placeholder:text-settings-muted focus-visible:ring-0"
							placeholder={t("shortcut.searchPlaceholder")}
							value={query}
							onChange={(event) => setQuery(event.target.value)}
						/>
					</label>
				</div>

				<div className="flex flex-col gap-2">
					{filteredShortcuts.map((shortcut) => {
						const bindings = effectiveShortcutBindings(shortcut.id, isMac, overrides);
						const modified = Object.hasOwn(overrides, shortcut.id);
						const isRecording = recording?.id === shortcut.id;
						return (
							<div
								className={cn(
									"flex min-h-(--size-settings-row) items-center rounded-lg bg-(--color-bg-settings-row) px-0 py-3",
									isRecording && "bg-settings-row-hover",
								)}
								key={shortcut.id}
							>
								<div className="flex min-w-0 flex-1 items-center gap-3">
									<div className="min-w-0 flex-1">
										<div className="flex items-center gap-2">
											<span className="text-sm font-medium text-settings-label">{shortcutLabel(shortcut.id, t)}</span>
											{modified ? (
												<span className="rounded-full bg-settings-menu-selected px-2 py-0.5 text-micro text-settings-muted">
													{t("shortcut.modified")}
												</span>
											) : null}
										</div>
										<span className="text-caption text-settings-muted">{shortcutCategoryLabel(shortcut.category, t)}</span>
									</div>

									{shortcut.customizable === false ? (
										<span className="ml-auto text-caption text-settings-muted">{t("shortcut.fixedIndexed")}</span>
									) : isRecording ? (
										<div className="ml-auto flex min-w-52 items-center gap-2 rounded-md border border-(--color-settings-accent) px-3 py-2 text-caption text-settings-label">
											<Keyboard className="size-icon-base animate-pulse" aria-hidden="true" />
											{t("shortcut.pressRecording")}
										</div>
									) : (
										<div className="ml-auto flex flex-wrap items-center justify-end gap-1.5">
											{bindings.length === 0 ? (
												<span className="text-caption text-settings-muted">{t("shortcut.unassigned")}</span>
											) : (
												bindings.map((candidate, index) => (
													<span
														className="inline-flex items-center rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input)"
														key={`${candidate.key}-${index}`}
													>
														<kbd className="px-2 py-1.5 font-mono text-caption text-settings-label">
															{shortcutBindingLabel(candidate, isMac)}
														</kbd>
														<Tooltip>
															<TooltipTrigger asChild>
																<button
																	type="button"
																	className="mr-1 inline-flex size-5 items-center justify-center rounded text-settings-muted hover:bg-settings-menu-selected hover:text-settings-label"
																	aria-label={t("shortcut.removeBindingAria", { binding: shortcutBindingLabel(candidate, isMac), name: shortcutLabel(shortcut.id, t) })}
																	onClick={() => void handleRemoveBinding(shortcut.id, index)}
																>
																	<X className="size-3" aria-hidden="true" />
																</button>
															</TooltipTrigger>
															<TooltipContent>{t("shortcut.remove")}</TooltipContent>
														</Tooltip>
													</span>
												))
											)}
											<Tooltip>
												<TooltipTrigger asChild>
													<button
														type="button"
														className="inline-flex size-8 items-center justify-center rounded-md text-settings-muted hover:bg-settings-menu-selected hover:text-settings-label"
														aria-label={t("shortcut.changeAria", { name: shortcutLabel(shortcut.id, t) })}
														onClick={() => void beginRecording({ id: shortcut.id, mode: "replace" })}
													>
														<Pencil className="size-icon-base" aria-hidden="true" />
													</button>
												</TooltipTrigger>
												<TooltipContent>{t("shortcut.change")}</TooltipContent>
											</Tooltip>
											{bindings.length < 2 ? (
												<Tooltip>
													<TooltipTrigger asChild>
														<button
															type="button"
															className="inline-flex size-8 items-center justify-center rounded-md text-settings-muted hover:bg-settings-menu-selected hover:text-settings-label"
															aria-label={t("shortcut.addAltAria", { name: shortcutLabel(shortcut.id, t) })}
															onClick={() => void beginRecording({ id: shortcut.id, mode: "add" })}
														>
															<Plus className="size-icon-base" aria-hidden="true" />
														</button>
													</TooltipTrigger>
													<TooltipContent>{t("shortcut.add")}</TooltipContent>
												</Tooltip>
											) : null}
											{modified ? (
												<Tooltip>
													<TooltipTrigger asChild>
														<button
															type="button"
															className="inline-flex size-8 items-center justify-center rounded-md text-settings-muted hover:bg-settings-menu-selected hover:text-settings-label"
															aria-label={t("shortcut.resetAria", { name: shortcutLabel(shortcut.id, t) })}
															onClick={() => void handleResetBinding(shortcut.id)}
														>
															<RotateCcw className="size-icon-base" aria-hidden="true" />
														</button>
													</TooltipTrigger>
													<TooltipContent>{t("shortcut.reset")}</TooltipContent>
												</Tooltip>
											) : null}
										</div>
									)}
								</div>
							</div>
						);
					})}
					{filteredShortcuts.length === 0 ? (
						<p className="py-8 text-center text-sm text-settings-muted">{t("shortcut.noMatching")}</p>
					) : null}
				</div>

				<div className="flex items-center gap-3 pt-2">
					{confirmResetAll ? (
						<>
							<span className="mr-auto text-caption text-settings-muted">{t("shortcut.resetAllConfirm")}</span>
							<Button type="button" variant="footer" className="rounded-md" onClick={() => setConfirmResetAll(false)}>
								{t("confirm.cancel")}
							</Button>
							<Button
								type="button"
								variant="footer-primary"
								className="rounded-md"
								onClick={() => {
									void resetAll().then(() => {
										setConfirmResetAll(false);
										showToast({ title: t("shortcut.restoredDefaults") });
									});
								}}
							>
								{t("shortcut.resetAll")}
							</Button>
						</>
					) : (
						<Button
							type="button"
							variant="footer"
							className="rounded-md"
							disabled={Object.keys(overrides).length === 0}
							onClick={() => setConfirmResetAll(true)}
						>
							<RotateCcw className="size-icon-base" aria-hidden="true" />
							{t("shortcut.resetAll")}
						</Button>
					)}
				</div>

				{toast ? (
					<div
						className="pointer-events-auto sticky bottom-2 z-10 ml-auto flex w-[min(24rem,100%)] items-start gap-3 rounded-xl border border-(--color-border-settings-dialog) bg-settings-dialog px-4 py-3 shadow-[var(--shadow-settings-dialog)]"
						role="status"
						aria-live="polite"
						aria-atomic="true"
					>
						<Check className="mt-0.5 size-icon-base shrink-0 text-success" aria-hidden="true" />
						<div className="min-w-0 flex-1">
							<p className="text-sm font-medium text-settings-label">{toast.title}</p>
							{toast.body ? <p className="mt-0.5 text-caption text-settings-muted">{toast.body}</p> : null}
						</div>
						{toast.undo ? (
							<button
								type="button"
								className="text-caption font-medium text-settings-label hover:underline"
								onClick={() => void toast.undo?.()}
							>
								{t("shortcut.undo")}
							</button>
						) : null}
						<button
							type="button"
							className="text-settings-muted hover:text-settings-label"
							aria-label={t("shortcut.dismissNotification")}
							onClick={() => setToast(null)}
						>
							<X className="size-icon-base" aria-hidden="true" />
						</button>
					</div>
				) : null}
			</div>

			<ConfirmDialog
				open={conflict !== null}
				title={t("shortcut.alreadyInUse")}
				description={
					conflict
						? t("shortcut.conflictBody", {
								binding: shortcutBindingLabel(conflict.binding, isMac),
								from: shortcutLabel(conflict.conflictingId, t),
								to: shortcutLabel(conflict.targetId, t),
							})
						: ""
				}
				confirmLabel={t("shortcut.reassign")}
				onOpenChange={(next) => {
					if (!next) setConflict(null);
				}}
				onConfirm={() => {
					if (!conflict) return;
					const pending = conflict;
					setConflict(null);
					void applyBinding(
						pending.targetId,
						pending.binding,
						pending.mode,
						pending.conflictingId,
					).catch(() =>
						showToast({
							title: t("shortcut.couldNotReassign"),
							body: t("shortcut.previousBindingsActive"),
						}),
					);
				}}
			/>
		</>
	);
}
