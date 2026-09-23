// Baked default Sentry DSN for the mobile app, mirroring the desktop
// DEFAULT_SENTRY_DSN. A separate project (and DSN) from desktop so mobile crash
// waves, quotas, and native symbolication stay independent. A client DSN is a
// write-only ingest endpoint, safe to ship in the client; volume is bounded by
// the project's Sentry-side spike protection and rate limit.
//
// Overridable via EXPO_PUBLIC_SENTRY_DSN. Note: this only takes effect once the
// @sentry/react-native SDK is installed (npx expo install @sentry/react-native +
// config plugin + prebuild); until then sentry.ts stays a no-op.
export const DEFAULT_SENTRY_DSN =
	"https://1d7f28aaf986243fae2218db3be2812a@o4511955966361600.ingest.us.sentry.io/4511967513935872";
