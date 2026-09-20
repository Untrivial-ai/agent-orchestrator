import * as WebBrowser from "expo-web-browser";
import { Linking } from "react-native";
import { githubAppUrl } from "./githubLink";
import { haptics } from "./haptics";

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
	try {
		// Same task as AO, as androidx's Custom Tabs default: expo's own default
		// launches through a proxy in a second task, which in 57.0.3 outlives the tab
		// as a dead AO card in Recents. The trade: relaunching AO (singleTask) while
		// the tab is up tears the tab down instead of returning to it.
		await WebBrowser.openBrowserAsync(url, { createTask: false });
	} catch {
		await Linking.openURL(url).catch(() => haptics.error());
	}
}
