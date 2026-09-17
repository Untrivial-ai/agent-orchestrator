"use client";

import { AnimatePresence, motion } from "framer-motion";
import { Menu, X } from "lucide-react";
import { useEffect, useState } from "react";
import { HashLink } from "../../../HashLink/HashLink";
import {
	type NavLink,
	PRODUCT_LINKS,
	RESOURCE_LINKS,
} from "../../constants";

interface MobileNavProps {
	ctaButtons: React.ReactNode;
}

export function MobileNav({ ctaButtons }: MobileNavProps) {
	const [isOpen, setIsOpen] = useState(false);
	const close = () => setIsOpen(false);

	// Lock the page behind the panel and let Escape dismiss it.
	useEffect(() => {
		if (!isOpen) return;

		const previousOverflow = document.body.style.overflow;
		document.body.style.overflow = "hidden";

		const handleKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") setIsOpen(false);
		};
		window.addEventListener("keydown", handleKeyDown);

		return () => {
			document.body.style.overflow = previousOverflow;
			window.removeEventListener("keydown", handleKeyDown);
		};
	}, [isOpen]);

	// The trigger is hidden at lg, so close on the way up or the lock sticks.
	useEffect(() => {
		const query = window.matchMedia("(min-width: 1024px)");
		const sync = () => {
			if (query.matches) setIsOpen(false);
		};

		sync();
		query.addEventListener("change", sync);
		return () => query.removeEventListener("change", sync);
	}, []);

	return (
		<div className="lg:hidden flex items-center gap-2 select-none">
			<div className="hidden sm:flex items-center gap-2 shrink-0">
				{ctaButtons}
			</div>
			<button
				type="button"
				className="-mr-2.5 grid size-11 place-items-center text-muted-foreground hover:text-foreground transition-colors"
				onClick={() => setIsOpen((prev) => !prev)}
				aria-label={isOpen ? "Close menu" : "Open menu"}
				aria-expanded={isOpen}
				aria-controls="mobile-nav"
			>
				{isOpen ? <X className="size-5" /> : <Menu className="size-5" />}
			</button>

			<AnimatePresence>
				{isOpen && (
					<>
						<motion.div
							key="mobile-nav-backdrop"
							className="fixed inset-x-0 bottom-0 top-14 z-40 bg-background/70 backdrop-blur-sm"
							initial={{ opacity: 0 }}
							animate={{ opacity: 1 }}
							exit={{ opacity: 0 }}
							transition={{ duration: 0.2, ease: [0.22, 1, 0.36, 1] }}
							onClick={close}
							aria-hidden="true"
						/>
						<motion.div
							key="mobile-nav-panel"
							id="mobile-nav"
							className="absolute inset-x-0 top-14 z-50 overflow-hidden border-t border-border bg-background"
							initial={{ height: 0 }}
							animate={{ height: "auto" }}
							exit={{ height: 0 }}
							transition={{ duration: 0.24, ease: [0.22, 1, 0.36, 1] }}
						>
							<div className="max-h-[calc(100dvh-3.5rem)] overflow-y-auto overscroll-contain px-4 pb-[env(safe-area-inset-bottom)] sm:px-8 lg:px-[30px]">
								<div className="max-w-7xl mx-auto py-4 flex flex-col gap-6">
									<MobileSection
										title="Product"
										links={PRODUCT_LINKS}
										onNavigate={close}
									/>
									<MobileSection
										title="Resources"
										links={RESOURCE_LINKS}
										onNavigate={close}
									/>
									<div className="pt-4 border-t border-border flex flex-col gap-3">
										{ctaButtons}
									</div>
								</div>
							</div>
						</motion.div>
					</>
				)}
			</AnimatePresence>
		</div>
	);
}

function MobileSection({
	title,
	links,
	onNavigate,
}: {
	title?: string;
	links: NavLink[];
	onNavigate: () => void;
}) {
	return (
		<div className="flex flex-col gap-1">
			{title && (
				<p className="px-2 pb-1 text-xs tracking-[-0.5px] text-muted-foreground/70">
					{title}
				</p>
			)}
			{links.map((link) =>
				link.external ? (
					<a
						key={link.href}
						href={link.href}
						target="_blank"
						rel="noopener noreferrer"
						className="flex min-h-11 items-center rounded-md px-2 text-sm text-muted-foreground hover:text-foreground transition-colors"
					>
						{link.label}
					</a>
				) : (
					<HashLink
						key={link.href}
						href={link.href}
						onClick={onNavigate}
						className="flex min-h-11 items-center rounded-md px-2 text-sm text-muted-foreground hover:text-foreground transition-colors"
					>
						{link.label}
					</HashLink>
				),
			)}
		</div>
	);
}
