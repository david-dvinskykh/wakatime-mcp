#!/usr/bin/env node
// Launcher for the wakatime-durations Go binary, so the MCP server can be
// started with `npx -y wakatime-durations-mcp`.
//
// Resolution order:
//   1. WAKATIME_DURATIONS_BIN, if set.
//   2. A cached binary for this package version.
//   3. The release binary from GitHub, verified against checksums.txt.
//   4. A build of the bundled Go source, if `go` is installed.
//
// stdout belongs to the MCP protocol: everything this script prints goes to
// stderr.
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');

const VERSION = require('../package.json').version;
const REPO = 'david-dvinskykh/wakatime-mcp';
const DOWNLOAD_BASE =
  process.env.WAKATIME_DURATIONS_DOWNLOAD_BASE ||
  `https://github.com/${REPO}/releases/download`;

function log(msg) {
  process.stderr.write(`wakatime-durations-mcp: ${msg}\n`);
}

function target() {
  const goos = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[process.platform];
  const goarch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
  if (!goos || !goarch) return null;
  const ext = goos === 'windows' ? '.exe' : '';
  return { goos, goarch, ext, asset: `wakatime-durations_${goos}_${goarch}${ext}` };
}

function cacheDir() {
  const base =
    process.env.WAKATIME_DURATIONS_CACHE ||
    process.env.XDG_CACHE_HOME ||
    (process.platform === 'darwin'
      ? path.join(os.homedir(), 'Library', 'Caches')
      : process.platform === 'win32'
        ? process.env.LOCALAPPDATA || path.join(os.homedir(), 'AppData', 'Local')
        : path.join(os.homedir(), '.cache'));
  return path.join(base, 'wakatime-durations', VERSION);
}

async function fetchBuffer(url) {
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) throw new Error(`GET ${url}: HTTP ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

function install(dest, data) {
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  const tmp = `${dest}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, data, { mode: 0o755 });
  fs.renameSync(tmp, dest);
}

async function download(t, dest) {
  const base = `${DOWNLOAD_BASE}/v${VERSION}`;
  const sums = (await fetchBuffer(`${base}/checksums.txt`)).toString('utf8');
  const line = sums.split('\n').find((l) => l.trim().split(/\s+/)[1] === t.asset);
  if (!line) throw new Error(`${t.asset} is not listed in checksums.txt`);
  const want = line.trim().split(/\s+/)[0].toLowerCase();
  const bin = await fetchBuffer(`${base}/${t.asset}`);
  const got = crypto.createHash('sha256').update(bin).digest('hex');
  if (got !== want) throw new Error(`checksum mismatch for ${t.asset}`);
  install(dest, bin);
}

function buildFromSource(dest) {
  const src = path.join(__dirname, '..', 'wakatime-durations');
  if (!fs.existsSync(path.join(src, 'go.mod'))) return false;
  const probe = spawnSync('go', ['version'], { stdio: 'ignore' });
  if (probe.error || probe.status !== 0) return false;
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  const tmp = `${dest}.${process.pid}.tmp`;
  log('building from source with Go (first run only)...');
  const r = spawnSync('go', ['build', '-trimpath', '-o', tmp, '.'], {
    cwd: src,
    stdio: ['ignore', 2, 2],
  });
  if (r.status !== 0) return false;
  fs.renameSync(tmp, dest);
  return true;
}

async function resolveBinary() {
  if (process.env.WAKATIME_DURATIONS_BIN) return process.env.WAKATIME_DURATIONS_BIN;
  const t = target();
  if (!t) throw new Error(`unsupported platform ${process.platform}/${process.arch}`);
  const dest = path.join(cacheDir(), `wakatime-durations${t.ext}`);
  if (fs.existsSync(dest)) return dest;

  let downloadErr;
  try {
    await download(t, dest);
    return dest;
  } catch (err) {
    downloadErr = err;
  }
  if (buildFromSource(dest)) return dest;
  throw new Error(
    `no binary for v${VERSION}: download failed (${downloadErr.message}) ` +
      'and Go is not installed to build from source (brew install go)'
  );
}

async function main() {
  let bin;
  try {
    bin = await resolveBinary();
  } catch (err) {
    log(err.message);
    process.exit(1);
  }
  const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });
  for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    process.on(sig, () => child.kill(sig));
  }
  child.on('error', (err) => {
    log(err.message);
    process.exit(1);
  });
  child.on('exit', (code, signal) => process.exit(signal ? 1 : code ?? 1));
}

main();
