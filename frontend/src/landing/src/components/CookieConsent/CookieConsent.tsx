"use client";

import { Button } from "@ao/ui/button";
import { AnimatePresence, motion } from "framer-motion";
import Link from "next/link";
import posthog from "posthog-js";
import { useEffect, useState } from "react";

import { ANALYTICS_CONSENT_KEY } from "@/lib/constants";

export function CookieConsent() {
	const [showBanner, setShowBanner] = useState(false);

	useEffect(() => {
		const consent = localStorage.getItem(ANALYTICS_CONSENT_KEY);
		if (consent === null) {
			setShowBanner(true);
		}
	}, []);

	const handleAccept = () => {
		localStorage.setItem(ANALYTICS_CONSENT_KEY, "accepted");
		setShowBanner(false);
		posthog.opt_in_capturing();
	};

	const handleOptOut = () => {
		localStorage.setItem(ANALYTICS_CONSENT_KEY, "declined");
		posthog.opt_out_capturing();
		setShowBanner(false);
	};

	return (
		<AnimatePresence>
			{showBanner && (
				<motion.div
					initial={{ y: 20, opacity: 0 }}
					animate={{ y: 0, opacity: 1 }}
					exit={{ y: 20, opacity: 0 }}
					transition={{ type: "spring", damping: 25, stiffness: 300 }}
					className="fixed inset-x-3 bottom-[calc(0.75rem+env(safe-area-inset-bottom))] z-50 rounded-lg border border-border bg-card p-3 shadow-lg sm:inset-x-auto sm:bottom-4 sm:left-4 sm:max-w-xs sm:p-4"
				>
					<p className="text-sm text-muted-foreground">
						We only collect analytics cookies so we can improve your experience.
					</p>
					<div className="mt-3 flex flex-wrap items-center justify-between gap-2">
						<Button variant="link" asChild className="px-0">
							<Link href="/privacy/">Privacy policy</Link>
						</Button>
						<div className="flex items-center gap-2">
							<Button variant="outline" onClick={handleOptOut}>
								Opt-out
							</Button>
							<Button variant="outline" onClick={handleAccept}>
								Accept
							</Button>
						</div>
					</div>
				</motion.div>
			)}
		</AnimatePresence>
	);
}
