import { Check, LoaderCircle, UserRound } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "./ui/dropdown-menu";
import { switchCodexSessionAccount, useCodexAccountsQuery } from "../hooks/useCodexAccountsQuery";
import { apiErrorMessage } from "../lib/api-client";

/**
 * Account pins are session-scoped. The global Codex account switcher updates
 * every existing route; this menu exposes the same account choices for a
 * session-local override and marks the currently active account.
 */
export function CodexSessionAccountMenuItems({ sessionId, enabled }: { sessionId: string; enabled: boolean }) {
	const { t } = useTranslation();
	const query = useCodexAccountsQuery(enabled);
	const [pendingAccountId, setPendingAccountId] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	if (!enabled || !query.data) return null;

	const accounts = query.data.accounts.filter(
		(account) => account.status === "valid" && account.authentication.state === "authorized",
	);
	if (accounts.length === 0) return null;

	return (
		<>
			<DropdownMenuSeparator />
			<DropdownMenuLabel>{t("settings.codexAccounts.switchConfirm")}</DropdownMenuLabel>
			{accounts.map((account) => {
				const pending = pendingAccountId === account.id;
				return (
					<DropdownMenuItem
						key={account.id}
						data-account-selected={account.active ? "true" : "false"}
						className={account.active ? "bg-interactive-hover text-foreground" : undefined}
						disabled={pendingAccountId !== null}
						onSelect={() => {
							setError(null);
							setPendingAccountId(account.id);
							void switchCodexSessionAccount(sessionId, account.id)
								.catch((switchError: unknown) => setError(apiErrorMessage(switchError, t("settings.codexAccounts.switch.failed"))))
								.finally(() => setPendingAccountId(null));
						}}
					>
						{pending ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : account.active ? <Check aria-hidden="true" className="text-success" /> : <UserRound aria-hidden="true" />}
						<div className="min-w-0">
							<p className="truncate text-foreground">{account.label}</p>
							{account.accountEmail ? <p className="truncate text-micro text-muted-foreground">{account.accountEmail}</p> : null}
						</div>
						{account.active ? <span className="ml-auto shrink-0 text-micro text-success">{t("settings.codexAccounts.inUse")}</span> : null}
					</DropdownMenuItem>
				);
			})}
			{error ? <DropdownMenuItem disabled className="whitespace-normal text-destructive">{error}</DropdownMenuItem> : null}
		</>
	);
}
