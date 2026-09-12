/**
 * Whether the app should show its loading indicator, given how far config
 * resolution has got.
 *
 * The distinction that matters is between *no config yet* and *no machine
 * paired*. They look identical — `config` is null either way — but they mean
 * opposite things to the user: one is "hold on", the other is "set me up".
 *
 * Treating the first as the second is what produced a black screen on open.
 * The poll effect turned the loader off whenever the config was unusable, and
 * the screen fell through to an empty list. That was invisible while resolving
 * meant a ~10ms read from storage; once resolution became an endpoint race it
 * lasted long enough to look broken, and longer still on a slow network.
 */
export function shouldShowLoading(s: { resolved: boolean; configured: boolean }): boolean {
	// Still working out how to reach the machine: the screen has nothing to show
	// yet, and the honest thing to show is that we are busy.
	if (!s.resolved) return true;
	// Resolution finished and there is nothing to connect to. Spinning forever
	// would hide the connect prompt the user actually needs.
	return s.configured;
}
