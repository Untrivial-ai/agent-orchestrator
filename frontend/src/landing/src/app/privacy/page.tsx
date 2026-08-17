import { COMPANY } from "@ao/shared/constants";
import type { Metadata } from "next";

const LAST_UPDATED = "30 July 2026";

const description =
  "How Agent Orchestrator handles data in AO Mobile, the desktop app and CLI, and aoagents.dev: local-first operation, optional analytics, and waitlist email processing.";

export const metadata: Metadata = {
  title: "Privacy Policy",
  description,
  openGraph: {
    type: "article",
    url: `${COMPANY.MARKETING_URL}/privacy/`,
    siteName: COMPANY.NAME,
    title: `Privacy Policy | ${COMPANY.NAME}`,
    description,
    images: [
      {
        url: `${COMPANY.MARKETING_URL}/og-image.png`,
        width: 1200,
        height: 630,
        alt: `${COMPANY.NAME} privacy policy`,
      },
    ],
  },
  twitter: {
    card: "summary",
    site: "@aoagents",
    title: `Privacy Policy | ${COMPANY.NAME}`,
    description,
    images: [`${COMPANY.MARKETING_URL}/og-image.png`],
  },
  alternates: {
    canonical: `${COMPANY.MARKETING_URL}/privacy/`,
  },
};

const ISSUES_URL = COMPANY.REPORT_ISSUE_URL;
const DISCORD_URL = COMPANY.DISCORD_URL;

function Section({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section
      id={id}
      className="scroll-mt-28 border-t border-border pt-10 first:border-t-0 first:pt-0"
    >
      <h2 className="text-[22px] font-semibold leading-tight tracking-[-0.02em] text-foreground sm:text-[26px]">
        {title}
      </h2>
      <div className="mt-5 space-y-4 text-[15px] leading-[1.75] text-muted-foreground sm:text-[16px]">
        {children}
      </div>
    </section>
  );
}

function Bullets({ children }: { children: React.ReactNode }) {
  return <ul className="space-y-3 pl-0">{children}</ul>;
}

function Bullet({ children }: { children: React.ReactNode }) {
  return (
    <li className="relative pl-5 before:absolute before:left-0 before:top-[0.7em] before:h-1 before:w-1 before:rounded-full before:bg-primary">
      {children}
    </li>
  );
}

function Strong({ children }: { children: React.ReactNode }) {
  return <strong className="font-semibold text-foreground">{children}</strong>;
}

function Ext({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="text-foreground underline decoration-primary decoration-1 underline-offset-4 transition-colors hover:text-primary"
    >
      {children}
    </a>
  );
}

function Code({ children }: { children: React.ReactNode }) {
  return (
    <code className="rounded-[4px] border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[0.86em] text-foreground">
      {children}
    </code>
  );
}

const toc = [
  { id: "scope", label: "What this covers" },
  { id: "mobile", label: "AO Mobile app" },
  { id: "desktop", label: "Desktop app & CLI" },
  { id: "website", label: "This website" },
  { id: "not-collected", label: "Data we do not collect" },
  { id: "third-parties", label: "Third-party services" },
  { id: "security", label: "Storage & security" },
  { id: "retention", label: "Retention & deletion" },
  { id: "rights", label: "Your rights" },
  { id: "children", label: "Children" },
  { id: "changes", label: "Changes" },
  { id: "contact", label: "Contact" },
];

export default function PrivacyPage() {
  return (
    <main className="relative min-h-screen">
      <header className="relative">
        <div className="relative mx-auto max-w-3xl px-6 pb-10 pt-16 md:pb-12 md:pt-20">
          <div className="font-mono text-sm tracking-[0.5px] text-muted-foreground">
            Legal
          </div>
          <h1 className="mt-4 text-[clamp(34px,5vw,54px)] font-semibold leading-[1.04] tracking-[-0.03em] text-foreground">
            Privacy Policy
          </h1>
          <p className="mt-4 font-mono text-[11px] uppercase tracking-[0.18em] text-muted-foreground">
            Last updated {LAST_UPDATED}
          </p>

          <div className="mt-8 rounded-[8px] border border-border bg-card/50 p-6 sm:p-7">
            <p className="text-[15px] leading-[1.75] text-muted-foreground sm:text-[16px]">
              <Strong>The short version.</Strong> Agent Orchestrator runs on your
              own machine. No account is required, and no hosted AO service stores
              your work. We never see your source code, prompts, agent output,
              terminal contents, repository names, or file paths, and we never
              sell or rent data to anyone. The desktop app sends{" "}
              <Strong>anonymous, redacted usage telemetry</Strong> so we can tell
              whether releases are stable — you can turn it off. Website analytics
              stay off until you accept them. If you voluntarily join a
              waitlist, we process the details you submit only for that
              purpose. The mobile app sends <Strong>no telemetry at all</Strong>{" "}
              and talks only to the server you point it at.
            </p>
          </div>

        </div>
      </header>

      <div className="relative mx-auto max-w-3xl px-6 pb-24 pt-12">
        <nav aria-label="On this page">
          <h2 className="font-mono text-[10px] uppercase tracking-[0.22em] text-muted-foreground">
            On this page
          </h2>
          <ul className="mt-4 grid gap-x-8 gap-y-2 sm:grid-cols-2">
            {toc.map((item) => (
              <li key={item.id}>
                <a
                  href={`#${item.id}`}
                  className="text-[14px] text-muted-foreground transition-colors hover:text-foreground"
                >
                  {item.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>

        <div className="mt-14 space-y-10">
          <Section id="scope" title="What this policy covers">
            <p>
              Agent Orchestrator ("AO") is open-source software published by the
              Untrivial-ai project. This policy applies to:
            </p>
            <Bullets>
              <Bullet>
                <Strong>AO Mobile</Strong> — the companion app for iOS and
                Android that connects to an AO daemon you run yourself.
              </Bullet>
              <Bullet>
                <Strong>The AO desktop app and CLI</Strong> — the local
                orchestrator that supervises coding agents in git worktrees on
                your computer.
              </Bullet>
              <Bullet>
                <Strong>aoagents.dev</Strong> — this website and the
                documentation hosted on it.
              </Bullet>
            </Bullets>
            <p>
              AO is not a hosted service. There is no AO account system and no
              AO server that stores your work. Everything AO orchestrates —
              repositories, worktrees, sessions, terminals, agent output — lives
              on hardware you control.
            </p>
            <p>
              The AI coding agents you run inside AO (Claude Code, Codex,
              Cursor, and others) are separate third-party tools with their own
              privacy policies. AO launches them locally; it does not intercept,
              store, or forward what they send to their own providers.
            </p>
          </Section>

          <Section id="mobile" title="AO Mobile (iOS and Android)">
            <p>
              AO Mobile lets you monitor and control an AO daemon that{" "}
              <Strong>you run yourself</Strong>, over your local network or a
              private network such as Tailscale. The app has no backend of its
              own; it talks only to the server you configure.
            </p>

            <h3 className="pt-2 text-[16px] font-semibold text-foreground">
              Data the app handles
            </h3>
            <Bullets>
              <Bullet>
                <Strong>Server connection details.</Strong> The host or address
                and port of your AO server, plus the connection password. The
                address and port are stored in the app's local storage; the
                password is stored in the device's secure keychain (iOS Keychain
                / Android Keystore). Both are sent only to the server you
                configure, in order to connect.
              </Bullet>
              <Bullet>
                <Strong>Camera (QR pairing).</Strong> With your permission, the
                camera is used solely to scan the pairing QR code shown by your
                server. No photos or video are stored, uploaded, or retained.
              </Bullet>
              <Bullet>
                <Strong>Push notification token.</Strong> To deliver
                notifications — for example when an agent is waiting for your
                input — the app requests a push token from the platform and
                registers it with <Strong>your own server</Strong>, so your
                server can notify you. The token is not sent anywhere else.
              </Bullet>
              <Bullet>
                <Strong>Device model and OS version.</Strong> Read on-device to
                request a valid push token and to render the interface
                correctly. Not transmitted to us.
              </Bullet>
              <Bullet>
                <Strong>Agent and project data.</Strong> Sessions, pull-request
                state, and terminal output are fetched from your own server for
                display. That data lives on your server; the app displays it and
                sends it nowhere else.
              </Bullet>
            </Bullets>

            <h3 className="pt-2 text-[16px] font-semibold text-foreground">
              How notifications work
            </h3>
            <p>
              When your server sends you a notification, it is relayed by the{" "}
              <Strong>Expo Push Service</Strong> to{" "}
              <Strong>Apple Push Notification service (APNs)</Strong> on iOS or{" "}
              <Strong>Firebase Cloud Messaging (FCM)</Strong> on Android, which
              deliver it to your device. The payload contains only what is
              needed to display and open the notification — a short title and
              body, and identifiers such as a session or pull-request reference.
              No passwords, tokens, or secrets are included. These platform
              services process the message only to deliver it, under their own
              privacy policies.
            </p>

            <p>
              AO Mobile contains{" "}
              <Strong>no analytics, advertising, or tracking SDKs</Strong>, and
              collects no usage telemetry whatsoever. Nothing in the app is used
              for tracking across apps or websites owned by other companies.
            </p>
          </Section>

          <Section id="desktop" title="Desktop app and CLI">
            <p>
              The desktop app and CLI run entirely on your machine. All
              application state — projects, worktrees, sessions, terminal
              history, settings — is written under <Code>~/.ao</Code> on your
              own disk and is never uploaded to us.
            </p>
            <p>
              To understand reliability and which features are actually used,
              the desktop app sends{" "}
              <Strong>anonymous, sanitized usage events</Strong> to{" "}
              <Ext href="https://posthog.com/privacy">PostHog</Ext>.
              Specifically:
            </p>
            <Bullets>
              <Bullet>
                App activation (capped to one event per six-hour UTC slot per
                install and channel), screen or route views grouped into coarse
                surface names, and coarse UI actions such as creating a task or
                starting a session.
              </Bullet>
              <Bullet>
                Operational events from the local daemon: command invocation,
                session spawn and failure, waiting-for-input transitions, HTTP
                5xx errors, and crashes.
              </Bullet>
              <Bullet>
                Crash and exception reports, reduced to an error name and a
                coarse context label.
              </Bullet>
              <Bullet>
                AO version, operating system platform, and build mode.
              </Bullet>
            </Bullets>
            <p>Before anything leaves your machine:</p>
            <Bullets>
              <Bullet>
                Absolute file paths (<Code>/Users/…</Code>, <Code>/home/…</Code>
                , <Code>C:\…</Code>) are replaced with{" "}
                <Code>[redacted-local-path]</Code>.
              </Bullet>
              <Bullet>
                Local URLs (<Code>file://</Code>, <Code>localhost</Code>,{" "}
                <Code>127.0.0.1</Code>) are replaced with{" "}
                <Code>[redacted-local-url]</Code>.
              </Bullet>
              <Bullet>
                Project and session identifiers are one-way hashed (SHA-256) and
                never sent in plain text.
              </Bullet>
              <Bullet>
                Daemon events pass through a strict allowlist, so only
                known-safe fields are ever exported.
              </Bullet>
            </Bullets>
            <p>
              Events are sent as <Strong>anonymous</Strong> PostHog events — no
              person profiles are created and the app never calls{" "}
              <Code>identify()</Code>. A random install identifier generated on
              first run and stored at{" "}
              <Code>~/.ao/data/telemetry_install_id</Code> is used to
              deduplicate counts. It is not linked to any account, email, or
              name. Approximate country is derived by PostHog from the
              connection's IP address; AO itself never sends location data.
            </p>
            <p>
              The desktop app does <Strong>not</Strong> currently send PostHog{" "}
              <Strong>session recordings</Strong>. Session recording is disabled
              by default; if a time-boxed investigation enables it, local paths,
              local URLs, and network request names are masked before
              transmission. It would cover the AO interface only — never other
              applications, never your desktop, and never keystroke content.
            </p>

            <div className="rounded-[8px] border border-border bg-card/50 p-5">
              <p className="text-[15px] leading-[1.75] text-muted-foreground">
                <Strong>Turning telemetry off.</Strong> Set{" "}
                <Code>AO_TELEMETRY_EVENTS=off</Code> and{" "}
                <Code>AO_TELEMETRY_REMOTE=off</Code> in the daemon's environment
                to stop daemon events. Because AO is open source, you can also
                build it yourself with an empty <Code>VITE_AO_POSTHOG_KEY</Code>
                , which removes transmission entirely. See{" "}
                <Ext href="https://github.com/Untrivial-ai/agent-orchestrator/blob/main/docs/telemetry.md">
                  docs/telemetry.md
                </Ext>{" "}
                for the full, source-level detail.
              </p>
            </div>

            <p>
              If you connect a GitHub account for pull-request and CI awareness,
              AO uses your existing local GitHub credentials to talk to GitHub
              directly from your machine. Those credentials stay on your machine
              and are never transmitted to us.
            </p>
          </Section>

          <Section id="website" title="This website">
            <p>
              aoagents.dev is a static site and runs no advertising. It uses
              PostHog analytics cookies to understand site usage and improve the
              experience, but analytics collection is disabled by default until
              you select <Strong>Accept</Strong>. Selecting opt-out keeps
              collection disabled. The choice is stored in your browser's local
              storage, no PostHog person profile is created, and session
              recording is disabled on the marketing site.
            </p>
            <p>
              Optional waitlists are separate from analytics. When you submit
              one, the details requested by that form, such as your email
              address, requested platform, or company role, are sent to PostHog
              solely to manage that waitlist, even if you opted out of site
              analytics. Submitting a waitlist form does not enable analytics
              for later browsing. Fonts are self-hosted. Other services involved
              when you browse are:
            </p>
            <Bullets>
              <Bullet>
                <Strong>GitHub.</Strong> Your browser requests the public
                repository's star count and latest release from the GitHub API,
                which means GitHub sees the request.
              </Bullet>
              <Bullet>
                <Strong>Mux.</Strong> The product demo is played through an
                embedded Mux video player, which loads only when the page
                containing it is viewed.
              </Bullet>
            </Bullets>
            <p>
              Our hosting provider may keep standard server logs (IP address,
              user agent, requested URL) for security and abuse prevention, as
              any web server does. We do not use those logs to build profiles.
            </p>
          </Section>

          <Section id="not-collected" title="Data we do not collect">
            <Bullets>
              <Bullet>
                Your source code, diffs, commits, or repository contents.
              </Bullet>
              <Bullet>
                Your prompts, agent conversations, or agent output.
              </Bullet>
              <Bullet>
                Terminal contents, command history, or environment variables.
              </Bullet>
              <Bullet>
                File paths, project names, branch names, or repository names.
              </Bullet>
              <Bullet>
                API keys, tokens, passwords, or any other credential.
              </Bullet>
              <Bullet>
                Names or account information. The only email address or company
                role we collect is information you voluntarily submit through an
                optional waitlist.
              </Bullet>
              <Bullet>Precise location data.</Bullet>
              <Bullet>
                Anything used for advertising, ad targeting, or cross-app
                tracking.
              </Bullet>
            </Bullets>
            <p>
              We do not sell, rent, or share personal data with third parties
              for their own purposes.
            </p>
          </Section>

          <Section id="third-parties" title="Third-party services">
            <p>
              AO relies on a small number of services, each only to make a
              specific feature work:
            </p>
            <Bullets>
              <Bullet>
                <Strong>PostHog</Strong> — product analytics for the desktop
                app, CLI, and website, plus storage of voluntarily submitted
                waitlist details (
                <Ext href="https://posthog.com/privacy">privacy policy</Ext>).
              </Bullet>
              <Bullet>
                <Strong>Expo Push Service</Strong> — relays mobile push
                notifications (
                <Ext href="https://expo.dev/privacy">privacy policy</Ext>).
              </Bullet>
              <Bullet>
                <Strong>Apple Push Notification service</Strong> — delivers
                notifications on iOS (
                <Ext href="https://www.apple.com/legal/privacy/">
                  privacy policy
                </Ext>
                ).
              </Bullet>
              <Bullet>
                <Strong>Firebase Cloud Messaging (Google)</Strong> — delivers
                notifications on Android (
                <Ext href="https://policies.google.com/privacy">
                  privacy policy
                </Ext>
                ).
              </Bullet>
              <Bullet>
                <Strong>GitHub</Strong> — hosts the source code, releases, and
                this website (
                <Ext href="https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement">
                  privacy statement
                </Ext>
                ).
              </Bullet>
              <Bullet>
                <Strong>Mux</Strong> — serves the demo video on this site (
                <Ext href="https://www.mux.com/privacy">privacy policy</Ext>).
              </Bullet>
            </Bullets>
          </Section>

          <Section id="security" title="Storage and security">
            <p>
              On desktop, all AO state is stored under <Code>~/.ao</Code> on
              your own machine, protected by your operating system's file
              permissions. On mobile, configuration is stored in app-local
              storage and the connection password is held in the platform secure
              keychain rather than in plaintext.
            </p>
            <p>
              AO Mobile connects over the address and transport (HTTP or HTTPS)
              you configure. The optional LAN listener that serves the mobile
              app binds to your network only while you explicitly enable it, and
              always requires the connection password. Because the server is one{" "}
              <Strong>you</Strong> run, you are responsible for securing that
              machine and the network it is reachable over. We recommend a
              private network such as Tailscale rather than exposing the daemon
              to the public internet.
            </p>
            <p>
              No system is perfectly secure, but because AO holds no central
              store of your data, there is no AO-side database of user content
              that could be breached.
            </p>
          </Section>

          <Section id="retention" title="Data retention and deletion">
            <Bullets>
              <Bullet>
                <Strong>On your devices.</Strong> Data stays until you delete
                it. Uninstalling the mobile app, or clearing its data, removes
                stored settings and the keychain entry and invalidates the push
                token registered with your server. Deleting <Code>~/.ao</Code>{" "}
                removes all desktop state.
              </Bullet>
              <Bullet>
                <Strong>Local daemon telemetry.</Strong> Retained in a local
                SQLite database for 30 days, then discarded.
              </Bullet>
              <Bullet>
                <Strong>Anonymous analytics.</Strong> Retained by PostHog under
                their standard retention schedule. Because these events carry no
                identifier tied to you personally, we generally cannot link them
                back to an individual.
              </Bullet>
              <Bullet>
                <Strong>Waitlist details.</Strong> Retained in PostHog while
                needed to notify you about the relevant release or AO Cloud
                access, then deleted. You may request earlier deletion using the
                private contact address below.
              </Bullet>
            </Bullets>
          </Section>

          <Section id="rights" title="Your rights">
            <p>
              Depending on where you live, you may have rights to access,
              correct, export, or delete personal data about you, and to object
              to certain processing — for example under the GDPR or the
              CCPA/CPRA.
            </p>
            <p>
              In practice, nearly all data AO touches is already in your own
              hands: delete the app, delete <Code>~/.ao</Code>, and it is gone.
              For the anonymous telemetry, the most direct way to exercise
              control is to turn it off using the settings described above. If
              you submitted a waitlist email or believe we hold other data about
              you, contact us privately at{" "}
              <Ext href={COMPANY.MAIL_TO}>{COMPANY.MAIL_TO.replace("mailto:", "")}</Ext>{" "}
              and we will act on the request. We do not sell or share personal
              information as those terms are defined under US state privacy laws.
            </p>
          </Section>

          <Section id="children" title="Children">
            <p>
              AO is a developer tool intended for professional and hobbyist
              software developers. It is not directed to children under 13, and
              we do not knowingly collect personal information from children.
            </p>
          </Section>

          <Section id="changes" title="Changes to this policy">
            <p>
              We may update this policy as AO evolves. Material changes will be
              reflected here with a new "last updated" date, and the history of
              every revision is public in the project's git repository.
            </p>
          </Section>

          <Section id="contact" title="Contact">
            <p>
              Send privacy requests or information you do not want to make
              public to{" "}
              <Ext href={COMPANY.MAIL_TO}>{COMPANY.MAIL_TO.replace("mailto:", "")}</Ext>.
              General questions and corrections can also use these public
              channels:
            </p>
            <Bullets>
              <Bullet>
                <Ext href={ISSUES_URL}>Open a GitHub issue</Ext> — the fastest
                route, and the one we monitor.
              </Bullet>
              <Bullet>
                <Ext href={DISCORD_URL}>Join the Discord</Ext> — for questions
                that are not a bug report.
              </Bullet>
            </Bullets>
            <p className="text-muted-foreground">
              Agent Orchestrator is open-source software released under Apache
              2.0 and provided as-is. If this policy and the source code ever
              disagree, the source code is the truth — and you are welcome to
              read it.
            </p>
          </Section>
        </div>
      </div>
    </main>
  );
}
