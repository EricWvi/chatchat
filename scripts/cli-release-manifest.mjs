import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { pathToFileURL } from "node:url";

// Derive latest.json from the finished archives so installers verify
// exactly what was published. Archives are chatchat-<tag>-<os>-<arch>.
export async function generateManifest(directory, repository, version) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository) || !/^v[\w.-]+$/.test(version)) {
    throw new Error("Invalid release repository or version");
  }
  const prefix = `chatchat-${version}-`;
  const assets = [];
  for (const name of (await readdir(directory)).sort()) {
    const extension = name.endsWith(".tar.gz") ? ".tar.gz" : name.endsWith(".zip") ? ".zip" : undefined;
    if (!name.startsWith(prefix) || !extension) continue;
    const target = name.slice(prefix.length, -extension.length);
    if (!/^[a-z0-9]+-[a-z0-9]+$/.test(target) || assets.some((asset) => asset.target === target)) {
      throw new Error(`Invalid or duplicate release target: ${target}`);
    }
    const bytes = await readFile(join(directory, name));
    if (bytes.length === 0) throw new Error(`Empty release archive: ${name}`);
    assets.push({
      target,
      name,
      url: `https://github.com/${repository}/releases/download/${encodeURIComponent(version)}/${encodeURIComponent(name)}`,
      sha256: createHash("sha256").update(bytes).digest("hex"),
    });
  }
  if (assets.length === 0) throw new Error("No chatchat release archives found");
  const manifest = { version, assets };
  await writeFile(join(directory, "latest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [, script, directory, repository, version, ...extra] = process.argv;
  if (!directory || !repository || !version || extra.length) {
    throw new Error(`Usage: node ${basename(script)} <directory> <owner/repo> <tag>`);
  }
  await generateManifest(directory, repository, version);
}
