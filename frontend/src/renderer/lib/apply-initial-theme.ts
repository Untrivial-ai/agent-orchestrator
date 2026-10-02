import { applyDocumentTheme, applyDocumentThemeStyle, readStoredThemeStyle, resolveTheme } from "./theme";

const isOnboardingRoute = typeof window !== "undefined" && window.location.hash.startsWith("#/onboarding");

// Runs as the first main.tsx import, before styles.css, so data-theme and
// data-style-theme are set before token CSS paints (avoids a flash on load).
applyDocumentTheme(isOnboardingRoute ? "dark" : resolveTheme());
applyDocumentThemeStyle(isOnboardingRoute ? "orchestrate" : readStoredThemeStyle());
