import { useEffect, useState } from "react";
import { aoBridge } from "./bridge";
import type { CloudAccount } from "../../shared/cloud-account";

export type { CloudAccount };

export type CloudSessionStatus = "loading" | "authenticated" | "unauthenticated";

export interface UseCloudSessionResult {
  configured: boolean;
  session: CloudAccount | null;
  status: CloudSessionStatus;
  signIn: (returnTo?: string) => void;
  signOut: () => Promise<void>;
}

// The WorkOS AuthKit client id is public configuration (it appears in every
// sign-in URL), so a baked default keeps sign-in working without build-time
// setup; VITE_WORKOS_CLIENT_ID overrides it per build when needed.
const DEFAULT_WORKOS_CLIENT_ID = "client_01KZ3VRKC374HS91XGRDPT3671";

export function isCloudSignInConfigured(
  clientId = import.meta.env.VITE_WORKOS_CLIENT_ID ?? DEFAULT_WORKOS_CLIENT_ID,
): boolean {
  return Boolean(clientId?.trim());
}

export function useCloudSession(): UseCloudSessionResult {
  const configured = isCloudSignInConfigured();
  const [session, setSession] = useState<CloudAccount | null>(null);
  const [status, setStatus] = useState<CloudSessionStatus>("loading");

  useEffect(() => {
    let active = true;
    aoBridge.cloud.getSession().then((s) => {
      if (!active) return;
      setSession(s);
      setStatus(s ? "authenticated" : "unauthenticated");
    }).catch(() => {
      if (!active) return;
      setSession(null);
      setStatus("unauthenticated");
    });

    const unsub = aoBridge.cloud.onSessionChanged((s) => {
      setSession(s);
      setStatus(s ? "authenticated" : "unauthenticated");
    });

    return () => {
      active = false;
      unsub();
    };
  }, []);

  // Parked while the sidebar sign-in entry point is intentionally hidden.
  const signIn = () => {
    void aoBridge.cloud.signIn();
  };

  const signOut = async () => {
    await aoBridge.cloud.signOut();
    setSession(null);
    setStatus("unauthenticated");
  };

  return { configured, session, status, signIn, signOut };
}
