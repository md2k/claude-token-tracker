#!/usr/bin/env node
import { readFileSync } from 'fs';
import { execSync, spawn } from 'child_process';
import { existsSync } from 'fs';
import { homedir } from 'os';
import { join } from 'path';

// Read JSON from stdin
const input = JSON.parse(readFileSync(0, 'utf-8'));

const {
  model: { display_name: model = 'unknown' } = {},
  output_style: { name: style = 'default' } = {},
  workspace: { current_dir: cwd = '~', project_dir: project = '' } = {},
  transcript_path: transcriptPath = '',
  context_window: {
    used_percentage: usedPct = 0,
    context_window_size: ctxSize = 200000
  } = {},
  cost: {
    total_cost_usd: cost = 0,
    total_duration_ms: durationMs = 0,
    total_api_duration_ms: apiDurationMs = 0,
    total_lines_added: linesAdded = 0,
    total_lines_removed: linesRemoved = 0
  } = {},
  session_id: sessionId = '',
  agent: { name: agentName = '' } = {},
  exceeds_200k_tokens: exceeds200k = false
} = input;

// Configuration
const DAEMON_PORT = 7777;
const DAEMON_PATH = join(homedir(), '.claude', 'token-tracker');
const DAEMON_TIMEOUT = '5m';
const DAEMON_LOG_LEVEL = 'silent';
const HEALTH_TIMEOUT = 200;
const STARTUP_WAIT = 250;
const MAX_RETRIES = 3;
const RETRY_DELAY = 100;

// Path Truncation Configuration
const PATH_MAX_LENGTH = 40;
const PATH_SHORTEN_STRATEGY = true;

// Progress Bar Configuration
const BAR_WIDTH = 20;

// Helper function to check daemon health
function checkDaemonHealth() {
  try {
    const response = execSync(`curl -s --max-time ${HEALTH_TIMEOUT / 1000} http://localhost:${DAEMON_PORT}/health`, {
      encoding: 'utf-8',
      timeout: HEALTH_TIMEOUT + 100
    });
    const data = JSON.parse(response);
    return data.status === 'ok';
  } catch {
    return false;
  }
}

// Helper function to start daemon
function startDaemon() {
  if (!existsSync(DAEMON_PATH)) {
    return false;
  }

  try {
    spawn(DAEMON_PATH, [
      '--port', DAEMON_PORT.toString(),
      '--timeout', DAEMON_TIMEOUT,
      '--log-level', DAEMON_LOG_LEVEL
    ], {
      detached: true,
      stdio: 'ignore'
    }).unref();
    return true;
  } catch {
    return false;
  }
}

// Helper function to fetch tokens from daemon
function fetchTokensFromDaemon(path) {
  try {
    const response = execSync(
      `curl -s --max-time 1 "http://localhost:${DAEMON_PORT}/tokens?path=${encodeURIComponent(path)}"`,
      { encoding: 'utf-8', timeout: 1100 }
    );
    return JSON.parse(response);
  } catch {
    return null;
  }
}

// Get token usage with daemon integration
let totalInput = 0, totalOutput = 0, totalCacheRead = 0, totalCacheCreate = 0, lastCacheCreate = 0;
let cacheTier5m = 0, cacheTier1h = 0, lastTier5m = 0, lastTier1h = 0;
let webSearchCount = 0, webFetchCount = 0;
let invalidationCount = 0, totalTokensInvalidated = 0;
let tokenStatus = 'ok'; // 'ok', 'starting', 'unavailable'
let cacheRebuilding = false;
let cacheEvent = '';

if (transcriptPath) {
  let tokens = null;
  let daemonHealthy = checkDaemonHealth();

  if (!daemonHealthy) {
    if (startDaemon()) {
      tokenStatus = 'starting';
      const startTime = Date.now();
      while (Date.now() - startTime < STARTUP_WAIT) {
        if (checkDaemonHealth()) {
          daemonHealthy = true;
          tokenStatus = 'ok';
          break;
        }
      }
    }
  }

  if (daemonHealthy) {
    for (let i = 0; i < MAX_RETRIES && !tokens; i++) {
      tokens = fetchTokensFromDaemon(transcriptPath);
      if (!tokens && i < MAX_RETRIES - 1) {
        execSync(`sleep ${RETRY_DELAY / 1000}`);
      }
    }
  }

  if (!tokens && tokenStatus !== 'starting') {
    tokenStatus = 'unavailable';
  }

  if (tokens) {
    totalInput = tokens.input_tokens || 0;
    totalOutput = tokens.output_tokens || 0;
    totalCacheRead = tokens.cache_read_tokens || 0;
    totalCacheCreate = tokens.cache_create_tokens || 0;
    lastCacheCreate = tokens.last_cache_create_tokens || 0;
    cacheTier5m = tokens.cache_tier_5m_tokens || 0;
    cacheTier1h = tokens.cache_tier_1h_tokens || 0;
    lastTier5m = tokens.last_cache_tier_5m_tokens || 0;
    lastTier1h = tokens.last_cache_tier_1h_tokens || 0;
    webSearchCount = tokens.web_search_count || 0;
    webFetchCount = tokens.web_fetch_count || 0;
    cacheRebuilding = tokens.cache_rebuilding || false;
    cacheEvent = tokens.cache_event || '';
    invalidationCount = tokens.invalidation_count || 0;
    totalTokensInvalidated = tokens.total_tokens_invalidated || 0;
  } else if (tokenStatus === 'starting') {
    // Keep starting status
  } else {
    tokenStatus = 'unavailable';
  }
}

// ── Format helpers ──────────────────────────────────────────────────────────
const fmt = (n) => n >= 1e6 ? `${(n/1e6).toFixed(1)}m` : n >= 1e3 ? `${(n/1e3).toFixed(1)}k` : String(n);
const esc = (code) => `\x1b[${code}m`;
const dim = esc('2');
const reset = esc('0');
const sep = ` ${dim}│${reset} `;

// Intelligent path truncation (Powerlevel10k style)
function truncatePath(path, maxLength) {
  if (!PATH_SHORTEN_STRATEGY || path.length <= maxLength) return path;

  const segments = path.split('/').filter(s => s !== '');
  if (segments.length <= 2) return path;

  let importantIndex = 1;
  let maxLen = 0;

  for (let i = 1; i < Math.min(segments.length - 1, 4); i++) {
    if (segments[i].length > maxLen) {
      maxLen = segments[i].length;
      importantIndex = i;
    }
  }

  const truncated = segments.map((seg, i) => {
    if (i === 0) return seg;
    if (i === importantIndex) return seg;
    if (i === segments.length - 1) return seg;
    return seg[0] || seg;
  });

  let result = truncated.join('/');

  if (result.length > maxLength && importantIndex < segments.length) {
    const important = segments[importantIndex];
    if (important.length > 10) {
      const maxImportantLen = Math.max(8, maxLength - (result.length - important.length) - 1);
      truncated[importantIndex] = important.substring(0, maxImportantLen) + '…';
      result = truncated.join('/');
    }
  }

  return result;
}

// Calculate relative path
let relPath = cwd.replace(project, '');
if (relPath === cwd) relPath = cwd.replace(process.env.HOME, '~');
if (relPath === cwd) relPath = '.';
relPath = truncatePath(relPath, PATH_MAX_LENGTH);

// Get git info
let gitBranch = '', gitStatus = '';
try {
  gitBranch = execSync('git branch --show-current 2>/dev/null', { cwd, encoding: 'utf-8' }).trim();
  const status = execSync('git status --porcelain 2>/dev/null', { cwd, encoding: 'utf-8' });

  const staged = (status.match(/^[MADRC]/gm) || []).length;
  const modified = (status.match(/^ [MD]/gm) || []).length;
  const untracked = (status.match(/^\?\?/gm) || []).length;

  if (staged) gitStatus += `✚${staged}`;
  if (modified) gitStatus += `✘${modified}`;
  if (untracked) gitStatus += `?${untracked}`;
  if (gitStatus) gitStatus = ' ' + gitStatus;
} catch {}

// Format durations
function formatDuration(ms) {
  const sec = Math.floor(ms / 1000);
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}m${s.toString().padStart(2, '0')}s`;
}

const durationStr = formatDuration(durationMs);
const apiDurationStr = formatDuration(apiDurationMs);

// Format context window size (compact: 200k, 1m — no decimals for round numbers)
const ctxLabel = ctxSize >= 1e6
  ? (ctxSize % 1e6 === 0 ? `${ctxSize/1e6}m` : `${(ctxSize/1e6).toFixed(1)}m`)
  : `${Math.round(ctxSize/1e3)}k`;

// Cache net efficiency (accounts for cache write overhead)
const cacheNetEff = totalCacheRead > 0
  ? (((totalCacheRead - totalCacheCreate) / (totalInput + totalCacheRead)) * 100).toFixed(1)
  : '0.0';

// ── Progress bar ────────────────────────────────────────────────────────────
function progressBar(pct) {
  const filled = Math.round((pct / 100) * BAR_WIDTH);
  const empty = BAR_WIDTH - filled;

  // Color based on 20% bands
  let colorCode;
  if (pct < 20)       colorCode = '38;5;51';   // cyan
  else if (pct < 40)  colorCode = '38;5;82';   // green
  else if (pct < 60)  colorCode = '38;5;226';  // yellow
  else if (pct < 80)  colorCode = '38;5;208';  // orange
  else                colorCode = '38;5;196';  // red

  const bar = esc(colorCode)
    + '▓'.repeat(filled) + '░'.repeat(empty)
    + ` ${String(Math.round(pct)).padStart(3)}%`
    + reset;
  return bar;
}

// ── Dim placeholder helper ──────────────────────────────────────────────────
const na = (label) => `${dim}${label}${reset}`;

// ── Build Line 1 ───────────────────────────────────────────────────────────
// model │ ctx size │ agent │ cost │ duration (API: api_dur) │ session_id │ path │ git
const line1Parts = [];

// Model name — padded to 14 chars so model(14) + sep(3) + ctx(8) = 25 = progress bar width
line1Parts.push(`${dim}${model.padEnd(14)}${reset}`);

// Context window size
line1Parts.push(`${esc('38;5;245')}${ctxLabel} ctx${reset}`);

// Agent name — always present
line1Parts.push(agentName
  ? `${esc('38;5;141')}🤖 ${agentName}${reset}`
  : na('🤖 —'));

// Cost
line1Parts.push(`${esc('38;5;226')}$${cost.toFixed(4)}${reset}`);

// Duration with API duration — always show both slots
line1Parts.push(apiDurationMs > 0
  ? `${esc('38;5;141')}${durationStr} ${dim}(API: ${apiDurationStr})${reset}`
  : `${esc('38;5;141')}${durationStr} ${dim}(API: —)${reset}`);

// Session ID — always present, full length
line1Parts.push(sessionId
  ? `${dim}${sessionId}${reset}`
  : na('no session'));

// Path — show "." when empty
line1Parts.push(`${esc('38;5;39')}${relPath || '.'}${reset}`);

// Git branch — always present
line1Parts.push(gitBranch
  ? `${esc('38;5;214')} ${gitBranch}${reset}${esc('38;5;196')}${gitStatus}${reset}`
  : na(' —'));

const line1 = line1Parts.join(sep);

// ── Build Line 2 ───────────────────────────────────────────────────────────
// progress bar │ input↓ output↑ │ cache efficiency │ cache tiers │ web │ lines
const line2Parts = [];

// Progress bar — always present
line2Parts.push(progressBar(usedPct));

// Token indicator — only show source marker when we actually fetched data
const indicator = !transcriptPath ? '' : tokenStatus === 'ok' ? 'ᵈ' : '';

// Input/Output tokens — always present
if (tokenStatus === 'starting') {
  line2Parts.push(`${esc('38;5;226')}⏳ starting...${reset}`);
  line2Parts.push(na('⚡ —'));
  line2Parts.push(na('🗂  —'));
  line2Parts.push(na('—'));
} else if (tokenStatus === 'unavailable') {
  line2Parts.push(`${esc('38;5;196')}[tokens N/A]${reset}`);
  line2Parts.push(na('⚡ —'));
  line2Parts.push(na('🗂  —'));
  line2Parts.push(na('—'));
} else {
  // Input/Output — always show (0 if no data)
  line2Parts.push(
    `${esc('38;5;33')}${fmt(totalInput)}↓${reset} ${esc('38;5;213')}${fmt(totalOutput)}↑${indicator}${reset}`
  );

  // Cache net efficiency — always present
  line2Parts.push(totalCacheRead > 0
    ? `${esc('38;5;99')}⚡ ${fmt(totalCacheRead)} (${cacheNetEff}%)${indicator}${reset}`
    : na(`⚡ — (—%)${indicator}`));

  // Cache write: last per-tier + total — always present
  if (totalCacheCreate > 0) {
    const last5m = lastTier5m > 0 ? `+${fmt(lastTier5m)}` : '0';
    const last1h = lastTier1h > 0 ? `+${fmt(lastTier1h)}` : '0';
    line2Parts.push(
      `${esc('38;5;147')}🗂  ${last5m} ${dim}(5m)${reset}  ${esc('38;5;147')}${last1h} ${dim}(1h)${reset} ${dim}/${reset} ${esc('38;5;147')}${fmt(totalCacheCreate)}${indicator}${reset}`
    );
  } else {
    line2Parts.push(na(`🗂  0 (5m)  0 (1h) / 0${indicator}`));
  }

  // Cache event — always present
  if (cacheEvent) {
    // Color based on event type
    let evtColor = '38;5;245'; // default dim
    if (cacheEvent.startsWith('🔄')) evtColor = '38;5;196';      // red for invalidation
    else if (cacheEvent.startsWith('🆕')) evtColor = '38;5;226';  // yellow for start
    else if (cacheEvent.startsWith('⚡')) evtColor = '38;5;82';   // green for read
    else if (cacheEvent.startsWith('📈')) evtColor = '38;5;51';   // cyan for growth
    line2Parts.push(`${esc(evtColor)}${cacheEvent}${reset}`);
  } else {
    line2Parts.push(na('no event'));
  }
}

// Web search/fetch — always present
if (webSearchCount > 0 || webFetchCount > 0) {
  line2Parts.push(`${esc('38;5;117')}🔍 ${webSearchCount}  📥 ${webFetchCount}${reset}`);
} else {
  line2Parts.push(na('🔍 0  📥 0'));
}

// Lines added/removed — always present
if (linesAdded > 0 || linesRemoved > 0) {
  line2Parts.push(`${esc('38;5;82')}+${linesAdded}${reset} ${esc('38;5;196')}-${linesRemoved}${reset}`);
} else {
  line2Parts.push(na('+0 -0'));
}

// 200k warning — only shown when active (this is a warning, not a slot)
if (exceeds200k) {
  line2Parts.push(`${esc('38;5;196')}⚠200k${reset}`);
}

const line2 = line2Parts.join(sep);

// ── Output ──────────────────────────────────────────────────────────────────
console.log(line1);
console.log(line2);
