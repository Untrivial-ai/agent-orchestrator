import type { ReactNode, SVGProps } from "react";
import { cn } from "./utils";

type IconProps = SVGProps<SVGSVGElement>;

function Icon({
	children,
	className,
	name,
	...props
}: IconProps & {
	children: ReactNode;
	name: string;
}) {
	return (
		<svg
			aria-hidden="true"
			className={cn("lucide", `lucide-${name}`, className)}
			fill="none"
			height="24"
			stroke="currentColor"
			strokeLinecap="round"
			strokeLinejoin="round"
			strokeWidth="2"
			viewBox="0 0 24 24"
			width="24"
			xmlns="http://www.w3.org/2000/svg"
			{...props}
		>
			{children}
		</svg>
	);
}

export function ArrowUpRightIcon(props: IconProps) {
	return (
		<Icon name="arrow-up-right" {...props}>
			<path d="M7 7h10v10" />
			<path d="M7 17 17 7" />
		</Icon>
	);
}

export function ChevronIcon({
	direction,
	...props
}: IconProps & {
	direction: "down" | "right" | "up";
}) {
	return (
		<Icon name={`chevron-${direction}`} {...props}>
			{direction === "down" ? (
				<path d="m6 9 6 6 6-6" />
			) : direction === "up" ? (
				<path d="m18 15-6-6-6 6" />
			) : (
				<path d="m9 18 6-6-6-6" />
			)}
		</Icon>
	);
}

export function BotIcon(props: IconProps) {
	return (
		<Icon name="bot" {...props}>
			<path d="M12 8V4H8" />
			<rect width="16" height="12" x="4" y="8" rx="2" />
			<path d="M2 14h2" />
			<path d="M20 14h2" />
			<path d="M9 13v2" />
			<path d="M15 13v2" />
		</Icon>
	);
}

export function CheckIcon(props: IconProps) {
	return (
		<Icon name="check" {...props}>
			<path d="M20 6 9 17l-5-5" />
		</Icon>
	);
}

export function FileCodeIcon(props: IconProps) {
	return (
		<Icon name="file-code-2" {...props}>
			<path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z" />
			<polyline points="14 2 14 8 20 8" />
			<path d="m10 13-2 2 2 2" />
			<path d="m14 17 2-2-2-2" />
		</Icon>
	);
}

export function PlusIcon(props: IconProps) {
	return (
		<Icon name="plus" {...props}>
			<path d="M5 12h14" />
			<path d="M12 5v14" />
		</Icon>
	);
}

export function FileTextIcon(props: IconProps) {
	return (
		<Icon name="file-text" {...props}>
			<path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z" />
			<polyline points="14 2 14 8 20 8" />
			<line x1="16" x2="8" y1="13" y2="13" />
			<line x1="16" x2="8" y1="17" y2="17" />
			<line x1="10" x2="8" y1="9" y2="9" />
		</Icon>
	);
}

export function GitBranchIcon(props: IconProps) {
	return (
		<Icon name="git-branch" {...props}>
			<line x1="6" x2="6" y1="3" y2="15" />
			<circle cx="18" cy="6" r="3" />
			<circle cx="6" cy="18" r="3" />
			<path d="M18 9a9 9 0 0 1-9 9" />
		</Icon>
	);
}

export function GitPullRequestIcon(props: IconProps) {
	return (
		<Icon name="git-pull-request" {...props}>
			<circle cx="18" cy="18" r="3" />
			<circle cx="6" cy="6" r="3" />
			<path d="M13 6h3a2 2 0 0 1 2 2v7" />
			<path d="M6 9v12" />
		</Icon>
	);
}

export function GitMergeIcon(props: IconProps) {
	return (
		<Icon name="git-merge" {...props}>
			<circle cx="6" cy="6" r="3" />
			<circle cx="18" cy="18" r="3" />
			<path d="M6 9v3a6 6 0 0 0 6 6h3" />
			<path d="m15 15 3 3-3 3" />
		</Icon>
	);
}

export function GitPullRequestClosedIcon(props: IconProps) {
	return (
		<Icon name="git-pull-request-closed" {...props}>
			<circle cx="6" cy="6" r="3" />
			<path d="M6 9v12" />
			<path d="m15 9 6 6" />
			<path d="m21 9-6 6" />
		</Icon>
	);
}

export function GitPullRequestDraftIcon(props: IconProps) {
	return (
		<Icon name="git-pull-request-draft" {...props}>
			<circle cx="6" cy="6" r="3" />
			<path d="M6 9v12" />
			<path d="M18 6h.01" />
			<path d="M18 10v8" />
		</Icon>
	);
}

export function LoaderCircleIcon(props: IconProps) {
	return (
		<Icon name="loader-circle" {...props}>
			<path d="M21 12a9 9 0 1 1-6.219-8.56" />
		</Icon>
	);
}

export function MessageSquareIcon(props: IconProps) {
	return (
		<Icon name="message-square" {...props}>
			<path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4z" />
		</Icon>
	);
}

export function MoreHorizontalIcon(props: IconProps) {
	return (
		<Icon name="more-horizontal" {...props}>
			<circle cx="12" cy="12" r="1" />
			<circle cx="19" cy="12" r="1" />
			<circle cx="5" cy="12" r="1" />
		</Icon>
	);
}

export function PaperclipIcon(props: IconProps) {
	return (
		<Icon name="paperclip" {...props}>
			<path d="m16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551" />
		</Icon>
	);
}

export function XIcon(props: IconProps) {
	return (
		<Icon name="x" {...props}>
			<path d="M18 6 6 18" />
			<path d="m6 6 12 12" />
		</Icon>
	);
}
