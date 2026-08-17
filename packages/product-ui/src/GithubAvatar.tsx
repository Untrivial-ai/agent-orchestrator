import { useState } from "react";
import { cn } from "./utils";

export type GithubAvatarProps = {
	login: string;
	className?: string;
};

function initials(login: string): string {
	return login
		.replace(/^@/, "")
		.trim()
		.split(/[-_\s]+/)
		.filter(Boolean)
		.slice(0, 2)
		.map((part) => part[0]?.toUpperCase() ?? "")
		.join("") || "?";
}

export function GithubAvatar({ login, className }: GithubAvatarProps) {
	const normalizedLogin = login.replace(/^@/, "").trim();
	const [failed, setFailed] = useState(false);
	const avatarURL = normalizedLogin
		? `https://github.com/${encodeURIComponent(normalizedLogin)}.png?size=64`
		: "";

	if (avatarURL && !failed) {
		return (
			<img
				alt=""
				aria-hidden="true"
				className={cn("size-icon-sm shrink-0 rounded-full object-cover", className)}
				draggable={false}
				loading="lazy"
				onError={() => setFailed(true)}
				referrerPolicy="no-referrer"
				src={avatarURL}
			/>
		);
	}

	return (
		<span
			aria-hidden="true"
			className={cn("inline-flex size-icon-sm shrink-0 items-center justify-center rounded-full bg-muted text-micro font-semibold text-muted-foreground", className)}
		>
			{initials(normalizedLogin)}
		</span>
	);
}
