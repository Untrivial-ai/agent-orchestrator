package iossdk

// Guidance contains static, actionable instructions shown when Xcode is not
// installed. AO never downloads or redistributes Xcode itself.
type Guidance struct {
	AppStoreURL  string `json:"appStoreURL"`
	DeveloperURL string `json:"developerURL"`
	WhyMissing   string `json:"whyMissing"`
}

// DefaultGuidance contains the links and explanation shown when Xcode is missing.
var DefaultGuidance = Guidance{
	AppStoreURL:  "https://apps.apple.com/app/xcode/id497799835",
	DeveloperURL: "https://developer.apple.com/download/all/",
	WhyMissing:   "Xcode is required to build, run, and inspect iOS apps in the Simulator. Install it from the Mac App Store or your Apple Developer account.",
}
