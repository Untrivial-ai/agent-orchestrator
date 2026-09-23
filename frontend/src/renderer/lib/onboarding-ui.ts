// Shared layout tokens for onboarding-style modals and panels (new task,
// clone repo, add project, cloud credential/sign-in). Keeps shell classes in
// one place so cloud flows don't drift from the local onboarding language.

/** Centered Radix/shadcn dialog shell (CloudCredentialDialog, CloudLocalSignInDialog). */
export const centeredOnboardingDialogClass =
	"fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(560px,calc(100vw-24px))] max-w-none -translate-x-1/2 -translate-y-1/2 flex-col gap-0 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none";

/** Embedded panel inside the create-project source dialog (ImportSourcePicker, cloud card). */
export const onboardingPanelClass =
	"relative w-full max-w-[min(560px,calc(100vw-24px))] overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl";

export const onboardingPanelTitleClass = "settings-dialog-title px-4 pt-3";
export const onboardingPanelDescriptionClass =
	"px-4 pb-3 pt-1 text-[13px] leading-5 text-muted-foreground";
export const onboardingPanelBodyClass = "flex min-h-0 flex-col gap-5 overflow-y-auto px-4 pb-4";
export const onboardingFormLabelClass = "text-[13px] font-semibold text-[var(--color-text-import-title)]";
export const onboardingFieldHintClass = "text-pretty text-[12px] leading-5 text-muted-foreground";
export const onboardingFieldErrorClass = "text-pretty text-[12px] leading-5 text-destructive";
export const onboardingAlertErrorClass =
	"rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-pretty text-[12px] leading-5 text-destructive";
export const onboardingFooterActionsClass = "flex shrink-0 items-center justify-between gap-3 pt-3";
export const onboardingFooterActionsEndClass = "flex shrink-0 items-center justify-end gap-3 pt-3";
