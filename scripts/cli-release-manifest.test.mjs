import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { generateManifest } from "./cli-release-manifest.mjs";

async function releaseDir(files) {
  const dir = await mkdtemp(join(tmpdir(), "chatchat-release-"));
  for (const [name, data] of Object.entries(files)) await writeFile(join(dir, name), data);
  return dir;
}

test("lists every archive with its target, pinned url and checksum", async () => {
  const dir = await releaseDir({
    "chatchat-v0.1.0-linux-amd64.tar.gz": "linux",
    "chatchat-v0.1.0-windows-amd64.zip": "windows",
    SHA256SUMS: "ignored",
    "chatchat-v0.0.9-linux-amd64.tar.gz": "old release, ignored",
  });
  const manifest = await generateManifest(dir, "EricWvi/chatchat", "v0.1.0");
  assert.deepEqual(manifest, {
    version: "v0.1.0",
    assets: [
      {
        target: "linux-amd64",
        name: "chatchat-v0.1.0-linux-amd64.tar.gz",
        url: "https://github.com/EricWvi/chatchat/releases/download/v0.1.0/chatchat-v0.1.0-linux-amd64.tar.gz",
        sha256: createHash("sha256").update("linux").digest("hex"),
      },
      {
        target: "windows-amd64",
        name: "chatchat-v0.1.0-windows-amd64.zip",
        url: "https://github.com/EricWvi/chatchat/releases/download/v0.1.0/chatchat-v0.1.0-windows-amd64.zip",
        sha256: createHash("sha256").update("windows").digest("hex"),
      },
    ],
  });
  assert.deepEqual(JSON.parse(await readFile(join(dir, "latest.json"), "utf8")), manifest);
});

test("rejects bad input", async () => {
  const empty = await releaseDir({ "chatchat-v0.1.0-linux-amd64.tar.gz": "" });
  await assert.rejects(generateManifest(empty, "EricWvi/chatchat", "v0.1.0"), /Empty/);
  const none = await releaseDir({ "other.tar.gz": "x" });
  await assert.rejects(generateManifest(none, "EricWvi/chatchat", "v0.1.0"), /No chatchat/);
  const dup = await releaseDir({ "chatchat-v0.1.0-linux-amd64.tar.gz": "a", "chatchat-v0.1.0-linux-amd64.zip": "b" });
  await assert.rejects(generateManifest(dup, "EricWvi/chatchat", "v0.1.0"), /duplicate/);
  await assert.rejects(generateManifest(none, "not a repo", "v0.1.0"), /Invalid/);
  await assert.rejects(generateManifest(none, "EricWvi/chatchat", "0.1.0"), /Invalid/);
});
