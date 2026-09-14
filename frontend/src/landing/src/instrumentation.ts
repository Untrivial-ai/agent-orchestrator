import * as Sentry from "@sentry/nextjs";

export async function register() {
  // Sentry disabled for now
}

export const onRequestError = Sentry.captureRequestError;
