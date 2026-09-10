import { type ReactNode, useCallback, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";
import { useSuppressStrayFocusRing } from "../../hooks/useSuppressStrayFocusRing";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import {
	SETTINGS_MENU_ITEM,
	SETTINGS_MENU_SURFACE,
	SettingsMenuTrigger,
} from "./SettingsMenuTrigger";

export type SettingsOption<T extends string> = {
	value: T;
	label: string;
	icon?: ReactNode;
	disabled?: boolean;
};

export function SettingsOptionMenu<T extends string>({
	value,
	options,
	onChange,
	disabled,
	placeholder,
	renderMenuItem,
	renderTrigger,
	triggerClassName,
	menuClassName,
	menuItemClassName,
	menuAlign = "end",
	searchable = false,
	searchPlaceholder,
	"aria-label": ariaLabel,
}: {
	value: T;
	options: SettingsOption<T>[];
	onChange: (value: T) => void;
	disabled?: boolean;
	placeholder?: string;
	renderMenuItem?: (option: SettingsOption<T>, selected: boolean) => ReactNode;
	renderTrigger?: (selected: SettingsOption<T> | undefined, placeholder?: string) => ReactNode;
	triggerClassName?: string;
	menuClassName?: string;
	menuItemClassName?: string;
	menuAlign?: "start" | "center" | "end";
	searchable?: boolean;
	searchPlaceholder?: string;
	"aria-label": string;
}) {
	const { t } = useTranslation();
	const [search, setSearch] = useState("");
	const [menuOpen, setMenuOpen] = useState(false);
	const selected = options.find((option) => option.value === value);
	const normalizedSearch = search.trim().toLocaleLowerCase();
	const visibleOptions = normalizedSearch
		? options.filter((option) =>
				`${option.label} ${option.value}`.toLocaleLowerCase().includes(normalizedSearch),
			)
		: options;
	const scrollRef = useRef<HTMLDivElement>(null);
	const [canScrollDown, setCanScrollDown] = useState(false);
	const updateScrollCue = useCallback(() => {
		const element = scrollRef.current;
		setCanScrollDown(Boolean(element && element.scrollHeight - element.scrollTop > element.clientHeight + 1));
	}, []);
	useLayoutEffect(() => {
		if (!menuOpen) {
			setCanScrollDown(false);
			return;
		}
		updateScrollCue();
		const element = scrollRef.current;
		if (!element || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(updateScrollCue);
		observer.observe(element);
		return () => observer.disconnect();
	}, [menuOpen, updateScrollCue, visibleOptions.length]);
	const onCloseAutoFocus = useSuppressStrayFocusRing(menuOpen);

	return (
		<DropdownMenu
			onOpenChange={(open) => {
				setMenuOpen(open);
				if (!open) setSearch("");
			}}
		>
			<DropdownMenuTrigger asChild disabled={disabled}>
				<SettingsMenuTrigger className={triggerClassName} aria-label={ariaLabel}>
					{renderTrigger ? (
						renderTrigger(selected, placeholder)
					) : (
						<>
							{selected?.icon}
							<span className="min-w-0 truncate">{selected?.label ?? placeholder}</span>
						</>
					)}
				</SettingsMenuTrigger>
			</DropdownMenuTrigger>
			{/* This menu scrolls on an inner element, so the panel itself must not
			    inherit the surface utility's overflow-y. */}
			<DropdownMenuContent
				align={menuAlign}
				alignOffset={0}
				onCloseAutoFocus={onCloseAutoFocus}
				className={cn(SETTINGS_MENU_SURFACE, "overflow-hidden!", menuClassName)}
			>
				{searchable && (
					<div className="shrink-0 p-1" onKeyDown={(event) => event.stopPropagation()}>
						<input
							type="search"
							aria-label={t("settings.options.searchAria", { label: ariaLabel.toLocaleLowerCase() })}
							value={search}
							onChange={(event) => setSearch(event.target.value)}
							placeholder={searchPlaceholder ?? t("settings.options.searchPlaceholder")}
							className="settings-inline-input w-full"
						/>
					</div>
				)}
				<div className="relative min-h-0">
					<div
						ref={scrollRef}
						className="model-menu-scroll max-h-select-menu-max overflow-y-auto overscroll-contain"
						onScroll={updateScrollCue}
					>
						{visibleOptions.map((option) => (
							<DropdownMenuItem
								key={option.value}
								disabled={option.disabled}
								onSelect={() => onChange(option.value)}
								className={cn(
									SETTINGS_MENU_ITEM,
									option.value === value && "border-settings-menu bg-settings-menu-selected text-settings-title",
									menuItemClassName,
								)}
							>
								{renderMenuItem ? (
									renderMenuItem(option, option.value === value)
								) : (
									<>
										{option.icon}
										{option.label}
									</>
								)}
							</DropdownMenuItem>
						))}
						{visibleOptions.length === 0 && (
							<p className="px-2 py-1.5 text-xs text-settings-muted">{t("settings.options.noMatches")}</p>
						)}
					</div>
					<div
						className={cn("model-menu-overflow-cue", canScrollDown ? "opacity-100" : "opacity-0")}
						aria-hidden="true"
					/>
				</div>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
