// Non-model source-contract check against the installed Pi, never an install/update.
// node adapter/pi/testdata/probe-restrictions.mjs /path/to/pi-package /path/to/pi
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { spawnSync } from "node:child_process";

const [pkg, binary] = process.argv.slice(2);
assert.ok(pkg && binary, "supply the installed package directory and executable");
assert.equal(JSON.parse(readFileSync(join(pkg, "package.json"), "utf8")).version, "1.3.0", "reverify the source contract for another release");
const load = (path) => import(pathToFileURL(join(resolve(pkg), path)).href);
const { parseArgs } = await load("dist/cli/args.js");
const { DefaultResourceLoader } = await load("dist/core/resource-loader.js");
const { SettingsManager } = await load("dist/core/settings-manager.js");
const { builtInExtensions } = await load("dist/extensions/index.js");
const root = mkdtempSync(join(tmpdir(), "pi-probe-source-"));
try {
  const home = join(root, "home"), cwd = join(root, "scratch"), agentDir = join(home, ".pi", "agent");
  mkdirSync(join(agentDir, "extensions"), { recursive: true });
  mkdirSync(join(cwd, ".pi", "extensions"), { recursive: true });
  const marker = join(root, "extension-ran"), npmMarker = join(root, "npm-ran");
  const extension = `import { writeFileSync } from 'node:fs'; export default function() { writeFileSync(${JSON.stringify(marker)}, 'loaded'); }`;
  writeFileSync(join(agentDir, "extensions", "hostile.js"), extension);
  writeFileSync(join(cwd, ".pi", "extensions", "hostile.js"), extension);
  const npm = join(root, "npm.cjs");
  writeFileSync(npm, `
    const args = process.argv.slice(2);
    if (args[0] === 'root') { console.log(${JSON.stringify(join(root, "empty-node-modules"))}); }
    else if (args[0] === 'list') { console.log('{"dependencies":{}}'); }
    else { require('node:fs').writeFileSync(${JSON.stringify(npmMarker)}, JSON.stringify(args)); process.exit(1); }
  `);
  writeFileSync(join(agentDir, "settings.json"), JSON.stringify({
    defaultProjectTrust: "always", extensions: ["./extensions/hostile.js"],
    packages: ["npm:hostile-not-installed"], npmCommand: [process.execPath, npm],
  }));
  writeFileSync(join(agentDir, "mcp.json"), JSON.stringify({ mcpServers: { hostile: { command: "never-run" } } }));
  const flags = ["--no-tools", "--no-mcp", "--no-extensions", "--offline"];
  const parsed = parseArgs(flags);
  assert.equal(parsed.noTools, true);
  assert.equal(parsed.noMcp, true);
  assert.equal(parsed.noExtensions, true);
  assert.equal(parsed.offline, true);
  assert.deepEqual(parsed.diagnostics, []);
  process.env.PI_OFFLINE = "1"; // main.js sets this from --offline before resource/model loading.
  const loader = new DefaultResourceLoader({
    cwd, agentDir, settingsManager: SettingsManager.create(cwd, agentDir, { projectTrusted: true }),
    noExtensions: parsed.noExtensions, disabledBuiltinExtensions: parsed.noMcp ? ["mcp"] : undefined,
    extensionFactories: builtInExtensions,
  });
  await loader.reload();
  assert.deepEqual(loader.getExtensions().errors, []);
  assert.deepEqual(loader.getExtensions().extensions, [], "no discovered, configured or built-in extensions, including MCP");
  assert.equal(existsSync(marker), false);
  assert.equal(existsSync(npmMarker), false, "offline skips missing package installation during resolve");

  // Exercise the real CLI's flag-to-loader feed, not just the SDK option.
  const invoke = (args) => {
    const result = spawnSync(binary, [...args, "--help"], {
      cwd, env: { HOME: home, PATH: process.env.PATH }, encoding: "utf8", timeout: 15000,
    });
    assert.equal(result.status, 0, result.stderr);
  };
  invoke(flags);
  assert.equal(existsSync(marker), false, "CLI must not execute hostile extension factories");
  assert.equal(existsSync(npmMarker), false, "CLI must not install inherited missing packages");
  invoke(["--no-tools", "--no-mcp", "--offline"]);
  assert.equal(existsSync(marker), true, "positive control: no-tools/no-mcp alone still execute extensions");
  assert.equal(existsSync(npmMarker), false);
  console.log("PASS: Pi 1.3.0 probe restrictions, hostile inheritance and positive control (no model call)");
} finally {
  rmSync(root, { recursive: true, force: true });
}
