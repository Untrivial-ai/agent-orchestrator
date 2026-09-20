"use client";

import type { CSSProperties, ReactNode } from "react";

export const featurePreviewTokens = {
	// Exact dark-theme values from frontend/src/styles/tokens.css (:root).
	"--preview-background": "oklch(0.185 0.006 285.885)",
	"--preview-foreground": "oklch(0.985 0 0)",
	"--preview-card": "oklch(0.24 0.008 285.885)",
	"--preview-card-foreground": "oklch(0.985 0 0)",
	"--preview-primary": "oklch(0.92 0.004 286.32)",
	"--preview-primary-foreground": "oklch(0.21 0.006 285.885)",
	"--preview-muted": "oklch(0.274 0.006 286.033)",
	"--preview-muted-foreground": "oklch(0.705 0.015 286.067)",
	"--preview-accent": "oklch(0.274 0.006 286.033)",
	"--preview-border": "oklch(1 0 0 / 7%)",
	"--preview-border-strong": "oklch(1 0 0 / 4%)",
	"--preview-ring": "oklch(0.552 0.016 285.938)",
	"--preview-divider": "oklch(1 0 0 / 4%)",
	"--preview-input": "oklch(1 0 0 / 4%)",
	"--preview-sidebar": "oklch(0.155 0.005 285.823)",
	"--preview-sidebar-foreground": "oklch(0.985 0 0)",
	"--preview-sidebar-accent": "oklch(0.274 0.006 286.033)",
	"--preview-sidebar-hover": "color-mix(in oklch, oklch(0.985 0 0) 4%, transparent)",
	"--preview-passive": "oklch(0.442 0.017 285.786)",
	"--preview-raised": "oklch(0.274 0.006 286.033)",
} as CSSProperties;

export const previewStatus = {
	working: "#60a5fa", // --color-status-working
	warning: "#fb923c", // --color-status-needs-you
	success: "#4ade80", // --color-status-ready
	error: "oklch(0.704 0.191 22.216)", // --destructive
	accent: "#60a5fa",
} as const;

export function FeaturePreviewShell({
	children,
	className = "",
	title,
	trailing,
}: {
	children: ReactNode;
	className?: string;
	title: string;
	trailing?: ReactNode;
}) {
	return (
		<div
			className={`mx-auto w-full min-w-0 max-w-[570px] select-none overflow-hidden rounded-[20px] border border-[var(--preview-border)] bg-[var(--preview-background)] font-sans text-[var(--preview-foreground)] antialiased shadow-[0_24px_64px_-20px_rgba(0,0,0,0.8)] ${className}`}
			style={featurePreviewTokens}
		>
			<div className="flex h-9 items-center border-b border-[var(--preview-border)] bg-[var(--preview-background)] px-3">
				<div className="flex items-center gap-1.5" aria-hidden="true">
					<span className="size-2.5 rounded-full bg-[#ff5f57]" />
					<span className="size-2.5 rounded-full bg-[#ffbd2e]" />
					<span className="size-2.5 rounded-full bg-[#28c840]" />
				</div>
				<div className="ml-4 flex min-w-0 items-center gap-2">
					<img src="/ao-logo.svg" alt="" className="size-4" draggable="false" loading="lazy" decoding="async" />
					<span className="truncate text-[11px] font-semibold tracking-[-0.4px] text-[var(--preview-muted-foreground)]">
						{title}
					</span>
				</div>
				{trailing ? <div className="ml-auto min-w-0 shrink-0">{trailing}</div> : null}
			</div>
			{children}
		</div>
	);
}

export function StatusDot({
	color,
	pulse = false,
}: {
	color: string;
	pulse?: boolean;
}) {
	return (
		<span className="relative flex size-2 shrink-0">
			{pulse ? (
				<span
					className="absolute inline-flex size-full animate-ping rounded-full opacity-40"
					style={{ backgroundColor: color }}
				/>
			) : null}
			<span
				className="relative inline-flex size-2 rounded-full"
				style={{ backgroundColor: color }}
			/>
		</span>
	);
}
