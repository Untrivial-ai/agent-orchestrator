import { Linking } from "react-native";
import { githubAppUrl } from "./githubLink";
import { haptics } from "./haptics";

/**
 * The in-app browser, when the build actually has it.
 *
 * `expo-web-browser` is a native module, and its package throws while it is
 * being imported if the runtime has no `ExpoWebBrowser` to bind to. That is not
 * hypothetical: the dependency arrived in a branch whose `Podfile.lock` was
 * never regenerated, so the module was in `node_modules` and absent from the
 * app, and every GitHub tap took the whole screen down with a red box.
 *
 * Requiring it here, inside the guard, means a build without the pod falls back
 * to the system browser instead — which is what this function did before the
 * in-app browser existed.
 */
type WebBrowserModule = { openBrowserAsync(url: string, options?: { createTask?: boolean }): Promise<unknown> };

let webBrowser: Promise<WebBrowserModule | null> | undefined;

function inAppBrowser(): Promise<WebBrowserModule | null> {
	if (!webBrowser) {
		webBrowser = import("expo-web-browser")
			.then((module) => module as unknown as WebBrowserModule)
			.catch(() => null);
	}
	return webBrowser;
}

// Opens a github.com URL in the GitHub app when it's installed, and in the
// in-app browser otherwise (SFSafariViewController / Chrome Custom Tab, so the
// user never leaves AO). The app is preferred because it carries the user's
// session: a private repo or PR opened in a browser shows a login wall or a 404.
//
// Any web URL is accepted: for one the app has no screen for, this is just the
// in-app browser. If that refuses (on Android, nothing resolves the intent:
// `NoMatchingActivityException`) the system browser is the last hop, as it was
// before the in-app one existed. Every caller is a tap, so when nothing at all
// could open the link the tap says so with the error haptic rather than reading
// as a dead button. On iOS `canOpenURL` also needs "github" listed in
// LSApplicationQueriesSchemes (see app.json) or it always answers false.
//
// A URL that is not a web page can only be opened by the system: the iOS bug
// report arrives as `x-safari-https://` (see bugReportOpenUrl), which the
// in-app browser rejects.
export async function openGitHub(url: string): Promise<void> {
	const appUrl = githubAppUrl(url);
	if (appUrl) {
		try {
			if (await Linking.canOpenURL(appUrl)) {
				await Linking.openURL(appUrl);
				return;
			}
		} catch {
			// Fall through to the browser.
		}
	}
	if (!/^https?:\/\//i.test(url)) {
		await Linking.openURL(url).catch(() => haptics.error());
		return;
	}
	const browser = await inAppBrowser();
	if (!browser) {
		await Linking.openURL(url).catch(() => haptics.error());
		return;
	}
	try {
		// Same task as AO, as androidx's Custom Tabs default: expo's own default
		// launches through a proxy in a second task, which in 57.0.3 outlives the tab
		// as a dead AO card in Recents. The trade: relaunching AO (singleTask) while
		// the tab is up tears the tab down instead of returning to it.
		await browser.openBrowserAsync(url, { createTask: false });
	} catch {
		await Linking.openURL(url).catch(() => haptics.error());
	}
}
