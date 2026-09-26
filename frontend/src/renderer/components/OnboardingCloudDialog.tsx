import * as Dialog from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { useCloudSession } from "../lib/cloud-session";
import { CloudProjectCard, CloudSignInPanel } from "./CreateProjectFlow";

/** Creating a cloud project from onboarding. The dialog shell is transparent and
 *  the card or the sign-in panel draws the surface, which is how the create
 *  project flow already hosts both of them. */
export function OnboardingCloudDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
	const { t } = useTranslation();
	const { status, signIn } = useCloudSession();
	const signedIn = status === "authenticated";

	return (
		<Dialog.Root open onOpenChange={(next) => { if (!next) onClose(); }}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay z-[calc(var(--z-overlay)-1)] data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 border-0 bg-transparent p-0 shadow-none outline-none data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<Dialog.Title className="sr-only">{t("onboarding.createCloudProject")}</Dialog.Title>
					<Dialog.Description className="sr-only">
						{t("onboarding.cloudDialogDescription")}
					</Dialog.Description>
					<div className="flex w-full flex-col items-center gap-3">
					{signedIn ? (
							<CloudProjectCard
								dialog
								onAuthRequired={() => signIn()}
								onBack={onClose}
								onClose={onClose}
								onCreated={onCreated}
							/>
						) : (
							<CloudSignInPanel
								dialog
								disabled={false}
								onBack={onClose}
								onSignIn={() => signIn()}
							/>
						)}
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
