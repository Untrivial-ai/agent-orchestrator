import { KeyRound } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import {
	onboardingFieldErrorClass,
	onboardingFieldHintClass,
	onboardingFormLabelClass,
} from "../../lib/onboarding-ui";
import { cn } from "../../lib/utils";

type GitHubTokenFieldProps = {
	id: string;
	label: string;
	hint?: string;
	placeholder?: string;
	value: string;
	onChange: (value: string) => void;
	disabled?: boolean;
	error?: string | null;
	onSubmit?: () => void;
	submitLabel: string;
	submitDisabled?: boolean;
	submitVariant?: "outline" | "primary";
	showSubmitButton?: boolean;
	tone?: "default" | "warning";
	/** Omit the inset card shell (settings rows already provide framing). */
	bare?: boolean;
	className?: string;
};

/** GitHub PAT entry used across cloud onboarding and settings. */
export function GitHubTokenField({
	id,
	label,
	hint,
	placeholder = "github_pat_…",
	value,
	onChange,
	disabled = false,
	error,
	onSubmit,
	submitLabel,
	submitDisabled = false,
	submitVariant = "primary",
	showSubmitButton = true,
	tone = "default",
	bare = false,
	className,
}: GitHubTokenFieldProps) {
	const shellClass = bare
		? "flex flex-col gap-2"
		: tone === "warning"
			? "flex flex-col gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 p-4"
			: "flex flex-col gap-2 rounded-lg border border-border/50 bg-[var(--color-bg-import-card)] p-4";

	return (
		<div className={cn(shellClass, className)}>
			<Label htmlFor={id} className={onboardingFormLabelClass}>
				{label}
			</Label>
			{hint ? <p className={onboardingFieldHintClass}>{hint}</p> : null}
			<div className="flex items-center gap-2">
				<div className="relative flex-1">
					<span className="pointer-events-none absolute inset-y-0 left-3 flex w-4 items-center justify-center text-muted-foreground">
						<KeyRound className="size-4" aria-hidden="true" />
					</span>
					<Input
						id={id}
						type="password"
						autoComplete="off"
						spellCheck={false}
						className="bg-[var(--color-bg-import-card)] pl-10 font-mono text-[13px]"
						placeholder={placeholder}
						disabled={disabled}
						value={value}
						onChange={(event) => onChange(event.target.value)}
						onKeyDown={(event) => {
							if (event.key === "Enter" && onSubmit) {
								event.preventDefault();
								onSubmit();
							}
						}}
					/>
				</div>
				{onSubmit && showSubmitButton ? (
					<Button
						type="button"
						variant={submitVariant}
						disabled={submitDisabled || disabled || value.trim() === ""}
						onClick={onSubmit}
					>
						{submitLabel}
					</Button>
				) : null}
			</div>
			{error ? (
				<p className={onboardingFieldErrorClass} role="alert">
					{error}
				</p>
			) : null}
		</div>
	);
}
