import type { NextConfig } from "next";

const landingRoot = process.cwd();
const usesServerRuntime =
	Boolean(process.env.NEXT_PUBLIC_API_URL) ||
	process.env.NEXT_PUBLIC_AO_AUTH_MODE === "workos" ||
	process.env.AO_CLOUD_AUTH_MODE === "workos";

// GitHub Pages serves a static export (see .github/workflows/deploy-landing.yml).
// Cloud uses live Next route handlers, so its web app must opt out of static
// export even when it uses local email/password auth.
const config: NextConfig = {
	output: usesServerRuntime ? undefined : "export",
	reactStrictMode: true,
	trailingSlash: usesServerRuntime ? false : true,
	turbopack: {
		// Keep Windows development from selecting a parent checkout's lockfile as
		// the workspace root and watching unrelated files.
		root: landingRoot,
	},
	images: {
		unoptimized: true,
		qualities: [75, 80],
		remotePatterns: [
			{
				protocol: "https",
				hostname: "*.public.blob.vercel-storage.com",
			},
			{
				protocol: "https",
				hostname: "unavatar.io",
			},
		],
	},
};

export default config;
