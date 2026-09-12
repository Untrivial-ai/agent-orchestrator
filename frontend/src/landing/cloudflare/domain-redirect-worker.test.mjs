// @vitest-environment node
import { expect, test } from "vitest";
import worker from "./domain-redirect-worker.mjs";

test.each(["aoagents.dev", "www.aoagents.dev", "www.useao.dev"])(
  "%s redirects HTTP and HTTPS while preserving encoded paths and query parameters",
  (host) => {
    for (const protocol of ["http:", "https:"]) {
      for (const method of ["GET", "POST"]) {
        const response = worker.fetch(new Request(
          `${protocol}//${host}/docs/a%20b/?a=1&a=2&to=%2Fpath`, { method },
        ));
        expect(response.status).toBe(308);
        expect(response.headers.get("location")).toBe(
          "https://useao.dev/docs/a%20b/?a=1&a=2&to=%2Fpath",
        );
      }
    }
  },
);

test.each(["useao.dev", "api.aoagents.dev", "aoagents.dev.evil.test"])(
  "%s is not redirected",
  (host) => {
    expect(worker.fetch(new Request(`https://${host}/`)).status).toBe(404);
  },
);
