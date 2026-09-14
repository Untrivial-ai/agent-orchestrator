import type { NextConfig } from "next";

// GitHub Pages serves a static export (see .github/workflows/deploy-landing.yml).
// Features that need a Node server (rewrites, proxy.ts) are intentionally omitted.
const config: NextConfig = {
	output: "export",
	reactStrictMode: true,
	trailingSlash: true,
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
