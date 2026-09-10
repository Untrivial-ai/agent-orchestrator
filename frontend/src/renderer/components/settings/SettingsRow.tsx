import { Pencil, type LucideIcon } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";

function SettingsRowLabel({
	icon: Icon,
	label,
	description,
}: {
	icon?: LucideIcon;
	label: string;
	description?: string;
}) {
	if (description === undefined) {
		return (
			<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
				{Icon ? <Icon className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" /> : null}
				<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{label}</span>
			</div>
		);
	}
	return (
		<div className="flex min-w-0 shrink items-start gap-(--size-settings-row-icon-gap)">
			{Icon ? <Icon className="mt-0.5 size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" /> : null}
			<span className="min-w-0">
				<span className="block text-sm leading-5 text-settings-label">{label}</span>
				<span className="mt-0.5 block text-pretty text-xs leading-4 text-settings-muted">{description}</span>
			</span>
		</div>
	);
}

/**
 * Settings row bar: tokenized height, radius, padding, and icon gap.
 *
 * `description` adds a sub-label under the row label for controls whose effect
 * is not obvious from the name alone — "Automatic Updates" was read as "install
 * automatically" when it only governs downloading.
 */
export function SettingsRow({
	icon,
	label,
	description,
	children,
	className,
}: {
	icon?: LucideIcon;
	label: string;
	description?: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<div
			className={cn(
				"settings-row-bar",
				description !== undefined && "h-auto min-h-(--size-settings-row) items-start py-3",
				className,
			)}
		>
			<SettingsRowLabel icon={icon} label={label} description={description} />
			<div
				className={cn(
					"flex min-w-0 flex-1 items-center justify-end",
					description !== undefined && "self-center",
				)}
			>
				{children}
			</div>
		</div>
	);
}

/** Idle value + pencil; the input bar appears only after click. */
export function SettingsInlineInput({
	id,
	label,
	value,
	onChange,
	onCommit,
	onCancel,
	placeholder,
	className,
}: {
	id: string;
	label: string;
	value: string;
	onChange: (value: string) => void;
	onCommit?: (value: string) => void;
	onCancel?: () => void;
	placeholder?: string;
	className?: string;
}) {
	const { t } = useTranslation();
	const [editing, setEditing] = useState(false);
	const inputRef = useRef<HTMLInputElement>(null);

	useEffect(() => {
		if (!editing) return;
		const input = inputRef.current;
		if (!input) return;
		input.focus();
		input.select();
	}, [editing]);

	const finishEditing = () => {
		setEditing(false);
		onCommit?.(value);
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") {
			event.preventDefault();
			finishEditing();
			return;
		}
		if (event.key === "Escape") {
			event.preventDefault();
			event.stopPropagation();
			setEditing(false);
			onCancel?.();
		}
	};

	if (!editing) {
		return (
			<button
				type="button"
				className={cn("settings-inline-edit-trigger", className)}
				aria-label={t("settings.field.edit", { label })}
				onClick={() => setEditing(true)}
			>
				<span className="settings-row-value" title={value || placeholder}>
					{value || placeholder}
				</span>
				<Pencil className="settings-inline-edit-icon" aria-hidden="true" />
			</button>
		);
	}

	return (
		<input
			ref={inputRef}
			id={id}
			aria-label={label}
			className={cn("settings-inline-edit-input", className)}
			value={value}
			onChange={(event) => onChange(event.target.value)}
			onBlur={finishEditing}
			onKeyDown={onKeyDown}
			placeholder={placeholder}
		/>
	);
}

export function SettingsInputRow({
	icon,
	label,
	id,
	value,
	onChange,
	onCommit,
	onCancel,
	placeholder,
}: {
	icon?: LucideIcon;
	label: string;
	id: string;
	value: string;
	onChange: (value: string) => void;
	onCommit?: (value: string) => void;
	onCancel?: () => void;
	placeholder?: string;
}) {
	return (
		<SettingsRow icon={icon} label={label}>
			<SettingsInlineInput
				id={id}
				label={label}
				value={value}
				onChange={onChange}
				onCommit={onCommit}
				onCancel={onCancel}
				placeholder={placeholder}
			/>
		</SettingsRow>
	);
}
