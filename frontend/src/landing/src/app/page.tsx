import { COMPANY } from "@ao/shared/constants";
import type { Metadata } from "next";
import dynamic from "next/dynamic";
import {
  FAQPageJsonLd,
  HomeWebPageJsonLd,
} from "@/components/JsonLd";
import { getGitHubRepoStats } from "@/lib/github-stats";
import { FAQ_ITEMS } from "./components/FAQSection/constants";
import { HeroSection } from "./components/HeroSection";

const TrustedBySection = dynamic(() =>
  import("./components/TrustedBySection").then((mod) => mod.TrustedBySection),
);
const FeaturesSection = dynamic(() =>
  import("./components/FeaturesSection").then((mod) => mod.FeaturesSection),
);
const VideoSection = dynamic(() =>
  import("./components/VideoSection").then((mod) => mod.VideoSection),
);
const WallOfLoveSection = dynamic(() =>
  import("./components/WallOfLoveSection").then((mod) => mod.WallOfLoveSection),
);
const FAQSection = dynamic(() =>
  import("./components/FAQSection").then((mod) => mod.FAQSection),
);

export const metadata: Metadata = {
  alternates: {
    canonical: COMPANY.MARKETING_URL,
  },
};

export default async function Home() {
  const stats = await getGitHubRepoStats();

  return (
    <main className="flex flex-col bg-background">
      <FAQPageJsonLd items={FAQ_ITEMS} />
      <HomeWebPageJsonLd />
      <HeroSection initialStars={stats?.stars ?? null} />
      <TrustedBySection />
      <FeaturesSection />
      <VideoSection />
      <WallOfLoveSection />
      <FAQSection />
    </main>
  );
}
