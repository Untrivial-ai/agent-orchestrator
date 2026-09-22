import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	centeredOnboardingDialogClass,
	onboardingFieldErrorClass,
	onboardingFieldHintClass,
	onboardingFooterActionsEndClass,
	onboardingFormLabelClass,
} from "../lib/onboarding-ui";
import { cn } from "../lib/utils";
import { useCloudLocalAuth } from "../hooks/useCloudLocalAuth";
import { useLocalSignInDialogStore } from "../stores/local-signin-dialog-store";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";

type Mode = "signIn" | "register";
type Phase = "idle" | "submitting";

// Matches the control plane's local-auth minimum (password >= 12 chars). The CP
// re-validates; this is only immediate feedback so the register button stays
// disabled until the rule is met.
const MIN_PASSWORD_LENGTH = 12;

// Dev-only email/password sign-in against a loopback Docker control plane
// running AO_CLOUD_LOCAL_AUTH. Mounted once (CloudOnboardingGate) and driven by
// the shared store from the sidebar entry points. On success the main process
// pushes the session over cloud:sessionChanged, so useCloudSession flips to
// "authenticated" and this dialog simply closes.
export function CloudLocalSignInDialog() {
	const { t } = useTranslation();
	const { available, login, register } = useCloudLocalAuth();
	const open = useLocalSignInDialogStore((s) => s.open);
	const setOpen = useLocalSignInDialogStore((s) => s.setOpen);
	const closeDialog = useLocalSignInDialogStore((s) => s.closeDialog);

	const [mode, setMode] = useState<Mode>("signIn");
	const [email, setEmail] = useState("");
	const [displayName, setDisplayName] = useState("");
	const [password, setPassword] = useState("");
	const [orgSlug, setOrgSlug] = useState("");
	const [orgName, setOrgName] = useState("");
	const [phase, setPhase] = useState<Phase>("idle");
	const [error, setError] = useState<string | null>(null);

	// Reset the whole form each time the dialog opens so a reopen never shows a
	// stale password or a previous error.
	useEffect(() => {
		if (!open) return;
		setMode("signIn");
		setEmail("");
		setDisplayName("");
		setPassword("");
		setOrgSlug("");
		setOrgName("");
		setPhase("idle");
		setError(null);
	}, [open]);

	// If the dev+loopback gate stops applying while the dialog is open (e.g. the
	// control-plane URL changed), close it rather than leave a dead form.
	useEffect(() => {
		if (open && !available) closeDialog();
	}, [open, available, closeDialog]);

	const trimmed = {
		email: email.trim(),
		displayName: displayName.trim(),
		orgSlug: orgSlug.trim(),
		orgName: orgName.trim(),
	};

	const canSubmit =
		phase !== "submitting" &&
		trimmed.email !== "" &&
		password !== "" &&
		(mode === "signIn" ||
			(trimmed.displayName !== "" &&
				trimmed.orgSlug !== "" &&
				trimmed.orgName !== "" &&
				password.length >= MIN_PASSWORD_LENGTH));

	const busy = phase === "submitting";

	const submit = async () => {
		if (!canSubmit) return;
		setPhase("submitting");
		setError(null);
		try {
			if (mode === "signIn") {
				await login({ email: trimmed.email, password });
			} else {
				await register({
					email: trimmed.email,
					displayName: trimmed.displayName,
					password,
					orgSlug: trimmed.orgSlug,
					orgName: trimmed.orgName,
				});
			}
			closeDialog();
		} catch (err) {
			setPhase("idle");
			setError(err instanceof Error ? err.message : t("cloudLocalAuth.genericError"));
		}
	};

	const onEnter = (event: React.KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") void submit();
	};

	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogContent className={centeredOnboardingDialogClass} showCloseButton={false}>
				<DialogClose asChild>
					<button
						type="button"
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("common.close")}
						disabled={busy}
					>
						<X className="size-icon-base" aria-hidden="true" />
					</button>
				</DialogClose>

				<div className="flex items-center gap-2 px-4 pr-12 pt-3">
					<DialogTitle className="text-balance text-[18px] font-semibold text-foreground">{t("cloudLocalAuth.title")}</DialogTitle>
					<span className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
						{t("cloudLocalAuth.devBadge")}
					</span>
				</div>
				<DialogDescription className="px-4 pr-12 pt-1 text-pretty text-[13px] leading-5 text-muted-foreground">
					{t("cloudLocalAuth.description")}
				</DialogDescription>

				<Tabs
					value={mode}
					onValueChange={(next) => {
						setMode(next as Mode);
						setError(null);
					}}
				>
					<div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-4 pb-1 pt-4">
						<TabsList className="w-full" aria-label={t("cloudLocalAuth.title")}>
							<TabsTrigger value="signIn">{t("cloudLocalAuth.tabSignIn")}</TabsTrigger>
							<TabsTrigger value="register">{t("cloudLocalAuth.tabRegister")}</TabsTrigger>
						</TabsList>

						<div className="space-y-2">
							<Label htmlFor="cloud-local-email" className={onboardingFormLabelClass}>
								{t("cloudLocalAuth.email")}
							</Label>
							<Input
								id="cloud-local-email"
								type="email"
								autoComplete="off"
								spellCheck={false}
								className="text-[13px]"
								disabled={busy}
								value={email}
								onChange={(e) => setEmail(e.target.value)}
								onKeyDown={onEnter}
							/>
						</div>

						<TabsContent value="register" className="flex flex-col gap-4">
							<div className="space-y-2">
								<Label htmlFor="cloud-local-displayName" className={onboardingFormLabelClass}>
									{t("cloudLocalAuth.displayName")}
								</Label>
								<Input
									id="cloud-local-displayName"
									autoComplete="off"
									spellCheck={false}
									className="text-[13px]"
									disabled={busy}
									value={displayName}
									onChange={(e) => setDisplayName(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="cloud-local-orgSlug" className={onboardingFormLabelClass}>
									{t("cloudLocalAuth.orgSlug")}
								</Label>
								<Input
									id="cloud-local-orgSlug"
									autoComplete="off"
									spellCheck={false}
									className="text-[13px]"
									disabled={busy}
									value={orgSlug}
									onChange={(e) => setOrgSlug(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="cloud-local-orgName" className={onboardingFormLabelClass}>
									{t("cloudLocalAuth.orgName")}
								</Label>
								<Input
									id="cloud-local-orgName"
									autoComplete="off"
									spellCheck={false}
									className="text-[13px]"
									disabled={busy}
									value={orgName}
									onChange={(e) => setOrgName(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
						</TabsContent>

						<div className="space-y-2">
							<Label htmlFor="cloud-local-password" className={onboardingFormLabelClass}>
								{t("cloudLocalAuth.password")}
							</Label>
							<Input
								id="cloud-local-password"
								type="password"
								autoComplete="off"
								spellCheck={false}
								className="text-[13px]"
								disabled={busy}
								value={password}
								onChange={(e) => setPassword(e.target.value)}
								onKeyDown={onEnter}
							/>
							{mode === "register" ? (
								<p className={onboardingFieldHintClass}>{t("cloudLocalAuth.passwordHint")}</p>
							) : null}
						</div>

						{error ? (
							<p role="alert" className={onboardingFieldErrorClass}>
								{error}
							</p>
						) : null}
					</div>
				</Tabs>

				<div className={cn(onboardingFooterActionsEndClass, "px-4 pb-4")}>
					<DialogClose asChild>
						<Button type="button" variant="outline" disabled={busy}>
							{t("cloudLocalAuth.cancel")}
						</Button>
					</DialogClose>
					<Button type="button" variant="primary" disabled={!canSubmit} onClick={() => void submit()}>
						{phase === "submitting"
							? t("cloudLocalAuth.working")
							: mode === "signIn"
								? t("cloudLocalAuth.submitSignIn")
								: t("cloudLocalAuth.submitRegister")}
					</Button>
				</div>
			</DialogContent>
		</Dialog>
	);
}
