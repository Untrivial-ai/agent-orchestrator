import { useRouter } from "expo-router";
import { ThemePickerSheet } from "../../lib/ThemePickerSheet";
import { useThemeState } from "../../lib/ThemeProvider";

// Theme is global state, so this sheet needs no result bridge — it writes the
// preference itself and dismisses.
export default function ThemeSheetRoute() {
	const router = useRouter();
	const { preference, setPreference } = useThemeState();

	return <ThemePickerSheet preference={preference} onSelect={setPreference} onClose={() => router.back()} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
