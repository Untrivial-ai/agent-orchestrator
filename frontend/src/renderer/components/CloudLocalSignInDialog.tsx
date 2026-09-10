import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";
import { useCloudLocalAuth } from "../hooks/useCloudLocalAuth";
import { useLocalSignInDialogStore } from "../stores/local-signin-dialog-store";
import { cn } from "../lib/utils";

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
			<DialogContent className={settingsDialogContentClass}>
				<div className={settingsDialogHeaderClass}>
					<div className="flex items-center gap-2">
						<DialogTitle className="settings-dialog-title">{t("cloudLocalAuth.title")}</DialogTitle>
						<span className="rounded-full bg-muted px-2 py-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
							{t("cloudLocalAuth.devBadge")}
						</span>
					</div>
					<DialogDescription asChild>
						<div className="text-control leading-4 text-settings-muted">{t("cloudLocalAuth.description")}</div>
					</DialogDescription>
				</div>

				<Tabs
					value={mode}
					onValueChange={(next) => {
						setMode(next as Mode);
						setError(null);
					}}
				>
					<div className={cn(settingsDialogBodyClass, "flex flex-col gap-4")}>
						<TabsList className="w-full">
							<TabsTrigger value="signIn">{t("cloudLocalAuth.tabSignIn")}</TabsTrigger>
							<TabsTrigger value="register">{t("cloudLocalAuth.tabRegister")}</TabsTrigger>
						</TabsList>

						<div className="flex flex-col gap-1.5">
							<Label htmlFor="cloud-local-email">{t("cloudLocalAuth.email")}</Label>
							<Input
								id="cloud-local-email"
								type="email"
								autoComplete="off"
								spellCheck={false}
								value={email}
								onChange={(e) => setEmail(e.target.value)}
								onKeyDown={onEnter}
							/>
						</div>

						<TabsContent value="register" className="flex flex-col gap-4">
							<div className="flex flex-col gap-1.5">
								<Label htmlFor="cloud-local-displayName">{t("cloudLocalAuth.displayName")}</Label>
								<Input
									id="cloud-local-displayName"
									autoComplete="off"
									spellCheck={false}
									value={displayName}
									onChange={(e) => setDisplayName(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label htmlFor="cloud-local-orgSlug">{t("cloudLocalAuth.orgSlug")}</Label>
								<Input
									id="cloud-local-orgSlug"
									autoComplete="off"
									spellCheck={false}
									value={orgSlug}
									onChange={(e) => setOrgSlug(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label htmlFor="cloud-local-orgName">{t("cloudLocalAuth.orgName")}</Label>
								<Input
									id="cloud-local-orgName"
									autoComplete="off"
									spellCheck={false}
									value={orgName}
									onChange={(e) => setOrgName(e.target.value)}
									onKeyDown={onEnter}
								/>
							</div>
						</TabsContent>

						<div className="flex flex-col gap-1.5">
							<Label htmlFor="cloud-local-password">{t("cloudLocalAuth.password")}</Label>
							<Input
								id="cloud-local-password"
								type="password"
								autoComplete="off"
								spellCheck={false}
								value={password}
								onChange={(e) => setPassword(e.target.value)}
								onKeyDown={onEnter}
							/>
							{mode === "register" ? (
								<p className="text-caption leading-4 text-settings-muted">{t("cloudLocalAuth.passwordHint")}</p>
							) : null}
						</div>

						{error ? (
							<p role="alert" className="text-caption leading-4 text-error">
								{error}
							</p>
						) : null}
					</div>
				</Tabs>

				<div className={settingsDialogFooterClass}>
					<DialogClose asChild>
						<Button type="button" variant="footer">
							{t("cloudLocalAuth.cancel")}
						</Button>
					</DialogClose>
					<Button type="button" variant="footer-primary" disabled={!canSubmit} onClick={() => void submit()}>
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
