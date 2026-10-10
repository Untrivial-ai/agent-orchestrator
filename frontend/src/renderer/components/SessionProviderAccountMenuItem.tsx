import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { accountAction, providerAccountsKey, sessionAccountQueryOptions, useProviderAccounts } from "../hooks/useProviderAccounts";
import { AccountMenuItems } from "./AccountMenu";
import { DropdownMenuItem, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger } from "./ui/dropdown-menu";

// The account a session runs on, in the session menu; choosing another one moves the session.
export function SessionProviderAccountMenuItem({ sessionId }: { sessionId: string }) {
	const { t } = useTranslation();
	// Mounted only while the session menu is open, so usage is read just then.
	const accounts = useProviderAccounts(true, true).data?.accounts ?? [];
	const cache = useQueryClient();
	const route = useQuery({ ...sessionAccountQueryOptions(sessionId), refetchInterval: 5000 }).data;
	const move = useMutation({
		mutationFn: (accountId: string) => accountAction(accountId, { action: "assign-session", sessionId }),
		onSuccess: (_, accountId) => {
			cache.setQueryData(sessionAccountQueryOptions(sessionId).queryKey, { managed: true, accountId });
			void cache.invalidateQueries({ queryKey: providerAccountsKey });
		},
	});
	if (!route?.managed) return null;
	const provider = accounts.find((account) => account.id === route.accountId)?.provider;
	const choices = accounts.filter((account) => account.provider === provider && account.signedIn);
	const current = choices.find((account) => account.id === route.accountId);
	return (
		<DropdownMenuSub>
			<DropdownMenuSubTrigger disabled={move.isPending}>
				<UserRound aria-hidden="true" className="size-3.5" />
				<span>{t("providerAccounts.account")}</span>
				{current ? <span className="ml-auto max-w-28 truncate text-2xs text-passive">{current.displayName}</span> : null}
			</DropdownMenuSubTrigger>
			<DropdownMenuSubContent className="min-w-64 text-xs">
				<AccountMenuItems accounts={choices} selectedId={route.accountId} disabled={(account) => move.isPending || account.id === route.accountId} onSelect={(account) => move.mutate(account.id)} manage>
					{route.accountId ? null : <DropdownMenuItem disabled>{t("providerAccounts.loginRequired")}</DropdownMenuItem>}
				</AccountMenuItems>
			</DropdownMenuSubContent>
		</DropdownMenuSub>
	);
}
