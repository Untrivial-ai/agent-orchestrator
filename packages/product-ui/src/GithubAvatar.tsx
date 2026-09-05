import { scmUserAvatarUrl } from "./scm-avatar";
import { UserAvatar } from "./UserAvatar";

export type GithubAvatarProps = {
	login: string;
	avatarUrl?: string;
	className?: string;
};

export function GithubAvatar({ login, avatarUrl, className }: GithubAvatarProps) {
	const normalizedLogin = login.replace(/^@/, "").trim();
	const imageUrl = avatarUrl?.trim() || scmUserAvatarUrl("github", "https://github.com", normalizedLogin);
	return <UserAvatar className={className} imageUrl={imageUrl} name={normalizedLogin} />;
}
