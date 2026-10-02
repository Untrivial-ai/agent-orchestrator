import * as Dialog from "@radix-ui/react-dialog";
import { ChevronLeft, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useCloudSession } from "../lib/cloud-session";
import { CloudProjectCard } from "./CreateProjectFlow";
import { Button } from "./ui/button";

/** Creating a cloud project from onboarding. Built on Clone Repository's chrome:
 *  same surface, a header with a back control, the same body padding, and a
 *  footer holding the single action, so the two project dialogs read as one
 *  family. */
export function OnboardingCloudDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
	const { t } = useTranslation();
	const { status, signIn } = useCloudSession();
	const signedIn = status === "authenticated";

	return (
		<Dialog.Root open onOpenChange={(next) => { if (!next) onClose(); }}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay z-[calc(var(--z-overlay)-1)] data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<div className="relative flex shrink-0 items-center gap-3 px-4 pt-3">
						<Button
							type="button"
							variant="outline"
							size="icon"
							aria-label={t("createProject.cloneBack")}
							onClick={onClose}
						>
							<ChevronLeft className="size-4" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1 pr-8">
							<Dialog.Title className="text-balance text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{t("onboarding.createCloudProject")}
							</Dialog.Title>
							<Dialog.Description className="sr-only">
								{t("onboarding.cloudDialogDescription")}
							</Dialog.Description>
						</div>
						<button
							type="button"
							className="settings-close-button"
							aria-label={t("common.close")}
							onClick={onClose}
						>
							<X className="size-4" aria-hidden="true" />
						</button>
					</div>

					{signedIn ? (
						<CloudProjectCard onAuthRequired={() => signIn()} onBack={onClose} onCreated={onCreated} />
					) : (
						<>
							<div className="min-h-0 overflow-y-auto">
								<div className="space-y-4 px-4 pb-1 pt-4">
									<p className="text-pretty text-[13px] leading-5 text-[var(--color-text-import-subtitle)]">
										{t("createProject.cloudSignInPrompt")}
									</p>
								</div>
							</div>
							<div className="flex shrink-0 justify-end gap-2 px-4 pb-4 pt-3">
								<Button type="button" variant="primary" onClick={() => signIn()}>
									{t("shell.signInToAOCloud")}
								</Button>
							</div>
						</>
					)}
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
