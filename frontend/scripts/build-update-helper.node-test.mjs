import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { helperBuildOptions, buildUpdateHelper } from "./build-update-helper.mjs";

describe("macOS update helper build", () => {
  for (const [arch, cpu] of [["arm64", "arm64"], ["x64", "x86_64"]]) {
    it(`compiles ${arch} independently of the host architecture`, () => {
      const { output, args } = helperBuildOptions(arch, "/project/frontend");
      assert.equal(output, "/project/frontend/update-helper/ao-update-progress");
      assert.ok(args.includes(`${cpu}-apple-macosx11.0`));
      assert.ok(args.includes("/project/frontend/native/update-helper/UpdateProgressState.swift"));
      assert.ok(args.includes("/project/frontend/native/update-helper/main.swift"));
    });
  }
  it("rejects unsupported architectures before spawning", () => {
    assert.throws(() => helperBuildOptions("universal"), /Unsupported/);
  });
  it("refuses a non-macOS build instead of silently shipping a missing helper", () => {
    assert.throws(() => buildUpdateHelper({ platform: "linux", run: () => { throw new Error("must not spawn"); } }), /must be built on macOS/);
  });
});
