"use client";

import { useState } from "react";

interface WaitlistFormProps {
	heading?: string;
	description?: string;
}

export function WaitlistForm({ heading, description }: WaitlistFormProps) {
	const [email, setEmail] = useState("");
	const [submitted, setSubmitted] = useState(false);

	function handleSubmit(e: React.FormEvent) {
		e.preventDefault();
		if (!email) return;

		setSubmitted(true);
	}

	if (submitted) {
		return (
			<div>
				<h2 className="mb-2 text-xl font-medium text-foreground">
					You're on the list!
				</h2>
				<p className="text-sm text-muted-foreground">
					We'll notify you when Windows &amp; Linux support is ready.
				</p>
			</div>
		);
	}

	return (
		<>
			{heading && (
				<h2 className="mb-2 text-xl font-medium text-foreground">{heading}</h2>
			)}
			{description && (
				<p className="mb-6 text-sm text-muted-foreground">{description}</p>
			)}
			<form onSubmit={handleSubmit} className="flex flex-col gap-3">
				<input
					type="email"
					required
					placeholder="you@example.com"
					value={email}
					onChange={(e) => setEmail(e.target.value)}
					className="w-full rounded-xl border border-border bg-background px-4 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
				/>
				<button
					type="submit"
					className="w-full rounded-xl bg-foreground py-2.5 text-sm font-medium text-background transition-opacity hover:opacity-90"
				>
					Join waitlist
				</button>
				<p className="text-xs leading-relaxed text-muted-foreground">
					By joining, you ask us to notify you when Windows &amp; Linux support
					is ready. See our{" "}
					<a className="underline underline-offset-2" href="/privacy/">
						privacy policy
					</a>
					.
				</p>
			</form>
		</>
	);
}
