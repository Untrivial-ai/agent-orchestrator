import {
	APP_SHORTCUTS,
	effectiveShortcutBindings,
	SHORTCUT_CATEGORIES,
	shortcutBindingKeys,
} from "../../shared/shortcuts";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { shortcutCategoryLabelKeys, shortcutLabelKeys } from "../i18n/key-maps";
import type { AppShortcutId, ShortcutCategory } from "../../shared/shortcuts";
import { useCommandPaletteEnabled } from "../hooks/useCommandPaletteEnabled";
import { useKeybindingsStore } from "../stores/keybindings-store";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	settingsDialogContentClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Button } from "./ui/button";

function shortcutLabel(id: AppShortcutId, t: TFunction): string {
	return t(shortcutLabelKeys[id]);
}

function shortcutCategoryLabel(category: ShortcutCategory, t: TFunction): string {
	return t(shortcutCategoryLabelKeys[category]);
}


type KeyboardShortcutsDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onCustomize?: () => void;
	isMac?: boolean;
};

function isMacPlatform(): boolean {
	if (typeof navigator === "undefined") return false;
	const platform =
		(navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform;
	return platform.toLowerCase().includes("mac");
}

export function KeyboardShortcutsDialog({
	open,
	onOpenChange,
	onCustomize,
	isMac = isMacPlatform(),
}: KeyboardShortcutsDialogProps) {
	const { t } = useTranslation();
	const isCommandPaletteEnabled = useCommandPaletteEnabled();
	const overrides = useKeybindingsStore((state) => state.overrides);
	const availableShortcuts = APP_SHORTCUTS.filter(
		(shortcut) => shortcut.id !== "command-palette" || isCommandPaletteEnabled,
	);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className={settingsDialogContentClass}>
				<DialogHeader className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("shortcut.dialogTitle")}</DialogTitle>
					<DialogDescription className="text-xs text-muted-foreground">
						{t("shortcut.dialogDescription")}
					</DialogDescription>
				</DialogHeader>

				<div className="overflow-y-auto px-(--size-modal-padding) py-2">
					{SHORTCUT_CATEGORIES.map((category) => {
						const shortcuts = availableShortcuts.filter((shortcut) => shortcut.category === category);
						if (shortcuts.length === 0) return null;
						return (
							<section className="border-b border-border py-4 last:border-b-0" key={shortcutCategoryLabel(category, t)}>
								<h2 className="mb-2 font-mono text-micro font-semibold uppercase tracking-wide-lg text-passive">
									{shortcutCategoryLabel(category, t)}
								</h2>
								<div className="flex flex-col">
									{shortcuts.map((shortcut) => (
										<div className="flex min-h-11 items-center justify-between gap-5 py-1.5" key={shortcut.id}>
											<p className="min-w-0 text-control font-medium text-foreground">{shortcutLabel(shortcut.id, t)}</p>
											<div className="flex shrink-0 flex-col items-end gap-1">
												{effectiveShortcutBindings(shortcut.id, isMac, overrides).map((binding, bindingIndex) => {
													const keys = shortcutBindingKeys(binding, isMac);
													return (
														<div
															className="flex items-center gap-1"
															aria-label={keys.join("+")}
															key={`${binding.key}-${bindingIndex}`}
														>
															{keys.map((key) => (
																<kbd
																	className="inline-flex min-w-7 items-center justify-center rounded-sm border border-border-strong bg-surface px-1.5 py-1 font-mono text-caption font-medium text-muted-foreground shadow-sm"
																	key={key}
																>
																	{key}
																</kbd>
															))}
														</div>
													);
												})}
											</div>
										</div>
									))}
								</div>
							</section>
						);
					})}
				</div>
				{onCustomize ? (
					<div className="flex justify-end border-t border-[var(--color-border-settings-dialog-header)] p-(--size-modal-padding)">
						<Button type="button" variant="footer-primary" onClick={onCustomize}>
							{t("shortcut.customize")}
						</Button>
					</div>
				) : null}
			</DialogContent>
		</Dialog>
	);
}
