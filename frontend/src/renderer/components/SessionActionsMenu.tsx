import { MoreVertical } from "lucide-react";
import { Children, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { TopbarButton } from "./TopbarButton";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";

export function SessionActionsMenu({
	children,
	inlineStatus,
}: {
	children?: ReactNode;
	inlineStatus?: ReactNode;
}) {
	const { t } = useTranslation();
	const menuItems = Children.toArray(children).filter(Boolean);
	if (menuItems.length === 0 && !inlineStatus) return null;

	// While an interface switch is running, replace the ⋮ entirely with the
	// status spinner so handoff/cancel controls cannot interrupt a half-applied switch.
	if (inlineStatus) {
		return <div className="inline-flex shrink-0 items-center">{inlineStatus}</div>;
	}

	return (
		<div className="inline-flex shrink-0 items-center gap-0.5">
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<TopbarButton
						aria-label={t("session.actionsMenu")}
						className="size-7 !bg-transparent text-muted-foreground hover:!bg-transparent active:!bg-transparent focus:!bg-transparent data-[state=open]:!bg-transparent hover:text-foreground"
						data-session-actions-trigger
						title={t("session.actionsMenu")}
						type="button"
						variant="icon"
					>
						<MoreVertical aria-hidden="true" className="size-icon-md" />
					</TopbarButton>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end" className="min-w-44">
					{menuItems}
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
	);
}
