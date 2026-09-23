import posthog from "posthog-js";

import { getHeroFlagBootstrap } from "@/lib/analytics/hero-flag-bootstrap";
import {
  buildMarketingPostHogConfig,
  resolveMarketingPostHogHost,
} from "@/lib/analytics/posthog-config";
import { ANALYTICS_CONSENT_KEY } from "@/lib/constants";
import { applyMarketingConsent } from "@/lib/analytics/marketing-consent";
import { campaignProperties } from "@/lib/analytics/campaign";
import { launchContextFromBrowser } from "@/lib/analytics/launch/context";
import { launchSuperProperties } from "@/lib/analytics/launch/registration";

// NEXT_PUBLIC_* is inlined at build time. Until the deploy workflow passed this
// through, it was undefined on the deployed site and init was skipped entirely,
// which is why the marketing site recorded nothing at all.
const POSTHOG_KEY = process.env.NEXT_PUBLIC_POSTHOG_KEY;

if (POSTHOG_KEY) {
  const host = resolveMarketingPostHogHost(process.env.NEXT_PUBLIC_POSTHOG_HOST);
  posthog.init(POSTHOG_KEY, {
    ...buildMarketingPostHogConfig(host, getHeroFlagBootstrap()),
    // loaded runs synchronously during init, and opt_in_capturing emits the
    // opt-in event + initial pageview right away. Registering the super-
    // properties here, before the opt-in, keeps those first events tagged with
    // app_name=marketing rather than leaking untagged into the shared project.
    loaded: (posthog) => {
      registerLaunchContext(posthog);
      applyMarketingConsent(
        posthog,
        localStorage.getItem(ANALYTICS_CONSENT_KEY),
        window.location.hostname,
      );
    },
  });

  // Capture the entry URL's UTM campaign now, before any client-side navigation
  // can drop the query string. captureCampaign is idempotent, so the later reads
  // inside track() return this same stored value; doing it here only guarantees
  // the anchor is the arrival URL rather than wherever the first event fired.
  campaignProperties();
}

export const onRouterTransitionStart = () => {};

/**
 * Registers the launch attribution (normalized source, the visit's own
 * utm_campaign, device class) as super-properties.
 *
 * Must run before `applyMarketingConsent`: for an already-consented visitor,
 * `opt_in_capturing()` emits the initial pageview synchronously inside `loaded`,
 * so registering from a later React effect would send that first pageview — the
 * landing event of the whole launch funnel — unattributed. Same lesson as the
 * `app_name` registration in `marketing-consent.ts`.
 *
 * UTMs come from campaign.ts (capture-once, persisted for the tab session), so
 * a visitor who arrived tagged keeps their attribution across an untagged
 * reload. `campaign` is registered only when the visit carried one and is
 * actively unregistered otherwise (registered super-properties persist in the
 * PostHog cookie), so a stale `launch_day` cannot ride along on later direct
 * visits either. The register/unregister decision is `launchSuperProperties`
 * (pure, unit-tested).
 */
function registerLaunchContext(posthog: import("posthog-js").PostHog): void {
  const { register, unregister } = launchSuperProperties(
    launchContextFromBrowser(),
  );
  for (const key of unregister) posthog.unregister(key);
  posthog.register(register);
}
