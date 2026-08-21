package iossdk

// Guidance is shown to the user when Xcode is not installed, with actionable
// paths to install it.
type Guidance struct {
	// AppStoreURL is the URL to open in the system browser for Xcode.
	AppStoreURL string `json:"appStoreURL"`
	// DeveloperURL is a link to the Xcode download page for registered developers.
	DeveloperURL string `json:"developerURL"`
	// WhyMissing explains why AO needs Xcode in plain language.
	WhyMissing string `json:"whyMissing"`
}

// DefaultGuidance is the static guidance payload returned when Xcode is
// absent. These are immutable reference strings; the controller hard-codes
// them rather than reading from an external source.
var DefaultGuidance = &Guidance{
	AppStoreURL:  "https://developer.apple.com/xcode/",
	DeveloperURL: "https://developer.apple.com/download/all/",
	WhyMissing:   "Xcode is required to build, run, and inspect iOS apps in the Simulator. AO cannot auto-download Xcode. Install it from the Mac App Store or download from your Apple Developer account.",
}
