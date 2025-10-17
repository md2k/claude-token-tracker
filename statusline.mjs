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
  cost: {
    total_cost_usd: cost = 0,
    total_duration_ms: durationMs = 0,
    total_lines_added: linesAdded = 0,
    total_lines_removed: linesRemoved = 0
  } = {},
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

// Display Configuration - Set to false to hide sections
const SHOW_MODEL = true;              // Model name (e.g., "Sonnet 4.5")
const SHOW_PATH = true;               // Current directory path
const SHOW_GIT = true;                // Git branch and status
const SHOW_STYLE = true;              // Output style name
const SHOW_COST = true;               // Cost in USD
const SHOW_DURATION = true;           // Session duration
const SHOW_TOKENS_INPUT_OUTPUT = true; // Input/Output tokens
const SHOW_CACHE_READ = true;         // Cache read tokens and efficiency
const SHOW_CACHE_WRITE = true;        // Cache write tokens
const SHOW_LINES = true;              // Lines added/removed
const SHOW_200K_WARNING = true;       // Warning when exceeding 200k tokens

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

// Helper function to parse tokens directly from file (fallback)
function parseTokensDirectly(path) {
  try {
    const transcript = readFileSync(path, 'utf-8');
    const lines = transcript.split('\n');
    let totalInput = 0, totalOutput = 0, totalCacheRead = 0, totalCacheCreate = 0;

    for (const line of lines) {
      if (!line.includes('"usage"')) continue;
      try {
        const data = JSON.parse(line);
        const usage = data.message?.usage || data.usage;
        if (usage) {
          totalInput += usage.input_tokens || 0;
          totalOutput += usage.output_tokens || 0;
          totalCacheRead += usage.cache_read_input_tokens || 0;
          totalCacheCreate += usage.cache_creation_input_tokens || 0;
        }
      } catch {}
    }

    return {
      input_tokens: totalInput,
      output_tokens: totalOutput,
      cache_read_tokens: totalCacheRead,
      cache_create_tokens: totalCacheCreate
      // Note: last_cache_create_tokens not tracked in fallback mode for performance
    };
  } catch {
    return null;
  }
}

// Get token usage with daemon integration
let totalInput = 0, totalOutput = 0, totalCacheRead = 0, totalCacheCreate = 0, lastCacheCreate = 0;
let tokenStatus = 'ok'; // 'ok', 'starting', 'fallback', 'unavailable'
let cacheRebuilding = false;

if (transcriptPath) {
  // Try to get tokens from daemon
  let tokens = null;
  let daemonHealthy = checkDaemonHealth();

  if (!daemonHealthy) {
    // Daemon not running, try to start it
    if (startDaemon()) {
      tokenStatus = 'starting';
      // Wait for daemon to start
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

  // Retry fetching tokens
  if (daemonHealthy) {
    for (let i = 0; i < MAX_RETRIES && !tokens; i++) {
      tokens = fetchTokensFromDaemon(transcriptPath);
      if (!tokens && i < MAX_RETRIES - 1) {
        execSync(`sleep ${RETRY_DELAY / 1000}`);
      }
    }
  }

  // Fallback to direct parsing if daemon fails
  if (!tokens && tokenStatus !== 'starting') {
    tokenStatus = 'fallback';
    tokens = parseTokensDirectly(transcriptPath);
  }

  if (tokens) {
    totalInput = tokens.input_tokens || 0;
    totalOutput = tokens.output_tokens || 0;
    totalCacheRead = tokens.cache_read_tokens || 0;
    totalCacheCreate = tokens.cache_create_tokens || 0;
    lastCacheCreate = tokens.last_cache_create_tokens || 0;
    cacheRebuilding = tokens.cache_rebuilding || false;
  } else if (tokenStatus === 'starting') {
    // Keep starting status
  } else {
    tokenStatus = 'unavailable';
  }
}

// Format helpers
const fmt = (n) => n >= 1e6 ? `${(n/1e6).toFixed(1)}m` : n >= 1e3 ? `${(n/1e3).toFixed(1)}k` : n;
const esc = (code) => `\x1b[${code}m`;

// Calculate relative path
let relPath = cwd.replace(project, '');
if (relPath === cwd) relPath = cwd.replace(process.env.HOME, '~');
if (relPath === cwd) relPath = '.';

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

// Format duration
const durationSec = Math.floor(durationMs / 1000);
const durationStr = `${Math.floor(durationSec/60)}m${(durationSec%60).toString().padStart(2,'0')}s`;

// Cache efficiency with 2 decimal precision
const cacheEff = totalCacheRead > 0
  ? ((totalCacheRead / (totalInput + totalCacheRead)) * 100).toFixed(2)
  : '0.00';

// Build status line
let output = '';
const sections = [];

// Model name
if (SHOW_MODEL) {
  sections.push(`${esc('2')}${model}${esc('0')}`);
}

// Path
if (SHOW_PATH) {
  sections.push(`${esc('38;5;39')}${relPath}${esc('0')}`);
}

// Git branch and status
if (SHOW_GIT && gitBranch) {
  sections.push(`${esc('38;5;214')}${gitBranch}${esc('0')}${esc('38;5;196')}${gitStatus}${esc('0')}`);
}

// Output style
if (SHOW_STYLE) {
  sections.push(`${esc('38;5;76')}${style}${esc('0')}`);
}

// Cost
if (SHOW_COST) {
  sections.push(`${esc('38;5;226')}$${cost.toFixed(4)}${esc('0')}`);
}

// Duration
if (SHOW_DURATION) {
  sections.push(`${esc('38;5;141')}${durationStr}${esc('0')}`);
}

// Join sections with separator
output = sections.join(` ${esc('2')}│${esc('0')} `);

// Add tokens
if (tokenStatus === 'starting') {
  output += ` ${esc('2')}│${esc('0')} ${esc('38;5;226')}⏳ starting...${esc('0')}`;
} else if (tokenStatus === 'unavailable') {
  output += ` ${esc('2')}│${esc('0')} ${esc('38;5;196')}[tokens N/A]${esc('0')}`;
} else if (totalInput > 0 || totalOutput > 0) {
  const indicator = tokenStatus === 'fallback' ? 'ᶠ' : 'ᵈ';
  const rebuildIcon = cacheRebuilding ? ' 🔄' : '';

  // Input/Output tokens
  if (SHOW_TOKENS_INPUT_OUTPUT) {
    output += ` ${esc('2')}│${esc('0')} ${esc('38;5;33')}${fmt(totalInput)}↓${esc('0')} ${esc('38;5;213')}${fmt(totalOutput)}↑${indicator}${rebuildIcon}${esc('0')}`;
  }

  // Cache tokens
  if ((SHOW_CACHE_READ && totalCacheRead > 0) || (SHOW_CACHE_WRITE && totalCacheCreate > 0)) {
    output += ` ${esc('2')}│${esc('0')}`;

    // Cache read
    if (SHOW_CACHE_READ && totalCacheRead > 0) {
      output += ` ${esc('38;5;99')}⚡${fmt(totalCacheRead)} ${esc('2')}(${cacheEff}%)${indicator}${esc('0')}`;
    }

    // Cache write
    if (SHOW_CACHE_WRITE && totalCacheCreate > 0) {
      // Show last write and total: 🗂  +1.5k/151k or just total if last is 0
      if (lastCacheCreate > 0) {
        output += ` ${esc('38;5;147')}🗂  +${fmt(lastCacheCreate)}${esc('2')}/${esc('0')}${esc('38;5;147')}${fmt(totalCacheCreate)}${indicator}${esc('0')}`;
      } else {
        output += ` ${esc('38;5;147')}🗂  ${fmt(totalCacheCreate)}${indicator}${esc('0')}`;
      }
    }
  }
}

// Add lines
if (SHOW_LINES && linesAdded > 0) {
  output += ` ${esc('2')}│${esc('0')} ${esc('38;5;82')}+${linesAdded}${esc('0')}`;
  if (linesRemoved > 0) output += ` ${esc('38;5;196')}-${linesRemoved}${esc('0')}`;
}

// Add warning
if (SHOW_200K_WARNING && exceeds200k) {
  output += ` ${esc('38;5;196')}⚠200k${esc('0')}`;
}

console.log(output);
