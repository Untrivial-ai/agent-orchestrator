import { Check, LoaderCircle, UserRound } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "./ui/dropdown-menu";
import { switchCodexSessionAccount, useCodexAccountsQuery } from "../hooks/useCodexAccountsQuery";
import { apiErrorMessage } from "../lib/api-client";

const sessionAccountSelectionCache = new Map<string, string>();

/**
 * Account pins are session-scoped. The global Codex account switcher changes
 * the default for future sessions; this menu exposes the account choices for
 * a session-local override and marks the selected account.
 */
export function CodexSessionAccountMenuItems({ sessionId, enabled }: { sessionId: string; enabled: boolean }) {
	const { t } = useTranslation();
	const query = useCodexAccountsQuery(enabled);
	const [pendingAccountId, setPendingAccountId] = useState<string | null>(null);
	const [sessionAccountId, setSessionAccountId] = useState<string | null>(() => sessionAccountSelectionCache.get(sessionId) ?? null);
	const [error, setError] = useState<string | null>(null);
	useEffect(() => {
		const selected = sessionAccountSelectionCache.get(sessionId);
		if (selected) {
			setSessionAccountId(selected);
			return;
		}
		const active = query.data?.activeAccountId;
		if (active) {
			sessionAccountSelectionCache.set(sessionId, active);
			setSessionAccountId(active);
		}
	}, [query.data?.activeAccountId, sessionId]);
	if (!enabled || !query.data) return null;

	const accounts = query.data.accounts.filter(
		(account) => account.status === "valid" && account.authentication.state === "authorized",
	);
	if (accounts.length === 0) return null;
	const selectedAccountId = sessionAccountId ?? query.data.activeAccountId ?? accounts.find((account) => account.active)?.id;

	return (
		<>
			<DropdownMenuSeparator />
			<DropdownMenuLabel>{t("settings.codexAccounts.switchConfirm")}</DropdownMenuLabel>
			{accounts.map((account) => {
				const pending = pendingAccountId === account.id;
				return (
					<DropdownMenuItem
						key={account.id}
						data-account-selected={selectedAccountId === account.id ? "true" : "false"}
						className={selectedAccountId === account.id ? "bg-interactive-hover text-foreground" : undefined}
						disabled={pendingAccountId !== null}
						onSelect={() => {
							setError(null);
							setPendingAccountId(account.id);
							void switchCodexSessionAccount(sessionId, account.id)
								.then(() => {
									sessionAccountSelectionCache.set(sessionId, account.id);
									setSessionAccountId(account.id);
								})
								.catch((switchError: unknown) => setError(apiErrorMessage(switchError, t("settings.codexAccounts.switch.failed"))))
								.finally(() => setPendingAccountId(null));
						}}
					>
						{pending ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : selectedAccountId === account.id ? <Check aria-hidden="true" className="text-success" /> : <UserRound aria-hidden="true" />}
						<div className="min-w-0">
							<p className="truncate text-foreground">{account.label}</p>
							{account.accountEmail ? <p className="truncate text-micro text-muted-foreground">{account.accountEmail}</p> : null}
						</div>
						{selectedAccountId === account.id ? <span className="ml-auto shrink-0 text-micro text-success">{t("settings.codexAccounts.inUse")}</span> : null}
					</DropdownMenuItem>
				);
			})}
			{error ? <DropdownMenuItem disabled className="whitespace-normal text-destructive">{error}</DropdownMenuItem> : null}
		</>
	);
}
