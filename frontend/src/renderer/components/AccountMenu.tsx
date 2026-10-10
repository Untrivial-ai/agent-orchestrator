import { Check, SlidersHorizontal } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { accountHeadroom, type ProviderAccount } from "../hooks/useProviderAccounts";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "./ui/dropdown-menu";

type MenuItems = { accounts: ProviderAccount[]; selectedId?: string; markDefault?: boolean; disabled?: (account: ProviderAccount) => boolean; onSelect: (account: ProviderAccount) => void; manage?: boolean };
// The rows of every account menu: name, Default mark, room left. selectedId adds the tick column; manage adds the
// empty row, the caller's own rows and the way to the Accounts page.
export function AccountMenuItems({ accounts, selectedId, markDefault = true, disabled, onSelect, manage, children }: MenuItems & { children?: ReactNode }) {
	const { t } = useTranslation();
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	return (
		<>
			{accounts.map((account) => {
				const headroom = accountHeadroom(account);
				return (
					<DropdownMenuItem key={account.id} disabled={disabled?.(account)} onSelect={() => onSelect(account)}>
						{selectedId === undefined ? null : <Check aria-hidden="true" className={account.id === selectedId ? "size-3.5" : "invisible size-3.5"} />}
						<span className="min-w-0 flex-1 truncate">{account.displayName}</span>
						{markDefault && account.primary ? <span className="shrink-0 text-2xs text-muted-foreground">{t("providerAccounts.default")}</span> : null}
						{headroom === null ? null : (
							<span className={cn("shrink-0 pl-2 text-xs tabular-nums", headroom === 0 ? "text-status-needs-you" : headroom <= 20 ? "text-warning" : "text-muted-foreground")}>
								{headroom === 0 ? t("providerAccounts.usageReached") : t("providerAccounts.usageRemaining", { percent: headroom })}
							</span>
						)}
					</DropdownMenuItem>
				);
			})}
			{manage && !accounts.length ? <DropdownMenuItem disabled>{t("providerAccounts.noAccountsAvailable")}</DropdownMenuItem> : null}
			{children}
			{manage ? <DropdownMenuSeparator /> : null}
			{manage ? (
				<DropdownMenuItem onSelect={() => openGlobalSettings("accountManager")}>
					<SlidersHorizontal aria-hidden="true" className="size-3.5 text-muted-foreground" />
					<span>{t("providerAccounts.manageAccounts")}</span>
				</DropdownMenuItem>
			) : null}
		</>
	);
}
// A button that opens an account menu; the button is the child.
export function AccountMenu({ children, className = "min-w-60", ...items }: MenuItems & { children: ReactNode; className?: string }) {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
			<DropdownMenuContent align="end" className={cn("text-xs", className)}><AccountMenuItems {...items} /></DropdownMenuContent>
		</DropdownMenu>
	);
}
