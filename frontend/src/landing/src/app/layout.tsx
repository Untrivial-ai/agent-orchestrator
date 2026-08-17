import { COMPANY } from "@ao/shared/constants";
import { GeistSans } from "geist/font/sans";
import type { Metadata } from "next";
import { IBM_Plex_Mono } from "next/font/google";

import { CookieConsent } from "@/components/CookieConsent";
import {
  OrganizationJsonLd,
  SoftwareApplicationJsonLd,
  WebsiteJsonLd,
} from "@/components/JsonLd";

import { CTAButtons } from "./components/CTAButtons";
import { Footer } from "./components/Footer";
import { Header } from "./components/Header";
import { LaunchAnalytics } from "./components/LaunchAnalytics";
import "./globals.css";
import { Providers } from "./providers";

const ibmPlexMono = IBM_Plex_Mono({
  weight: ["300", "400", "500"],
  subsets: ["latin"],
  variable: "--font-ibm-plex-mono",
  display: "swap",
});

const siteDescription =
  "Run a fleet of coding agents without losing track of branches, reviews, or CI failures. Free and open source under Apache 2.0.";

export const metadata: Metadata = {
  metadataBase: new URL(COMPANY.MARKETING_URL),
  title: {
    default: `Run Coding Agents in Parallel | ${COMPANY.NAME}`,
    template: `%s | ${COMPANY.NAME}`,
  },
  description: siteDescription,
  keywords: [
    "coding agents",
    "agent orchestration",
    "parallel execution",
    "developer tools",
    "AI coding",
    "git worktrees",
    "code automation",
    "Claude Code",
    "Cursor",
    "Codex",
    "agent fleet",
    "PR automation",
  ],
  authors: [{ name: `${COMPANY.NAME} Team` }],
  creator: COMPANY.NAME,
  openGraph: {
    type: "website",
    locale: "en_US",
    url: COMPANY.MARKETING_URL,
    siteName: COMPANY.NAME,
    title: COMPANY.NAME,
    description: siteDescription,
    images: [
      {
        url: "/og-image.png",
        width: 1200,
        height: 630,
        alt: COMPANY.NAME,
      },
    ],
  },
  twitter: {
    card: "summary_large_image",
    title: COMPANY.NAME,
    description: siteDescription,
    images: ["/og-image.png"],
    creator: "@aoagents",
  },
  robots: {
    index: true,
    follow: true,
    googleBot: {
      index: true,
      follow: true,
      "max-video-preview": -1,
      "max-image-preview": "large",
      "max-snippet": -1,
    },
  },
  icons: {
    icon: [
      { url: "/favicon.svg", type: "image/svg+xml" },
    ],
  },
  manifest: "/manifest.json",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`dark overscroll-none ${ibmPlexMono.variable} ${GeistSans.variable}`}
      suppressHydrationWarning
    >
      <head>
        <OrganizationJsonLd />
        <SoftwareApplicationJsonLd />
        <WebsiteJsonLd />
        <link rel="preload" as="image" href="/optimized/hero-background.webp" type="image/webp" />
        <link rel="preload" as="image" href="/optimized/feature.webp" type="image/webp" />
        <link rel="preload" as="image" href="/optimized/feature2.webp" type="image/webp" />
        <link rel="preload" as="image" href="/optimized/feature3.webp" type="image/webp" />
        <link rel="preload" as="image" href="/optimized/feature4.webp" type="image/webp" />
        <script
          dangerouslySetInnerHTML={{
            __html: `
              (function() {
                var ua = navigator.userAgent || "";
                var isMobile = /iphone|ipad|ipod|android|mobile|tablet/i.test(ua) ||
                  (/macintosh/i.test(ua) && navigator.maxTouchPoints > 1);
                document.documentElement.dataset.landingPlatform =
                  !isMobile && /mac os x|macintosh/i.test(ua) ? "mac" : "other";
              })();
            `,
          }}
        />
      </head>
      <body className="relative overscroll-none font-sans antialiased">
        <Providers>
          <LaunchAnalytics />
          <Header ctaButtons={<CTAButtons />} />
          {children}
          <Footer />
          <CookieConsent />
        </Providers>
      </body>
    </html>
  );
}
