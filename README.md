# Claude Token Counter

A lightweight Go daemon that tracks token usage for Claude Code sessions in real-time.

<img width="1261" height="119" alt="image" src="https://github.com/user-attachments/assets/534030a3-a685-4835-8ac1-d36f61346dd3" />

## Features

- **Real-time token tracking** from Claude Code transcript files
- **In-memory caching** for ultra-fast statusline queries (~1-2ms)
- **Incremental parsing** - only processes new content
- **Cache invalidation detection** - alerts when prompt cache expires and rebuilds
- **Cache TTL countdown** - real-time countdown showing time until cache expiration with color-coded alerts
- **Intelligent path truncation** - Powerlevel10k-style directory shortening
- **Auto-cleanup** of inactive sessions
- **Graceful shutdown** after idle timeout (default: 10 minutes)
- **HTTP API** for easy integration
- **Detailed metrics** including parse times and file statistics
- **Transcript analyzer** - Python script for detailed per-message analysis, cache events, and cost tracking

## Requirements

- Go 1.16+ (for building)
- Claude Code CLI
- A Nerd Font for proper icon display (recommended: [MesloLGS NF](https://github.com/romkatv/powerlevel10k#meslo-nerd-font-patched-for-powerlevel10k))

## Installation

### Quick Install (Recommended)

```bash
# Clone the repository
git clone https://github.com/md2k/claude-token-counter.git
cd claude-token-counter

# Build and install (automatically detects ~/.claude directory)
make install
```

The `make install` command will:
- Build the `token-tracker` binary for your OS
- Install to `~/.claude/` if the directory exists
- Make scripts executable
- Show next steps

### Manual Installation

```bash
# Clone the repository
git clone https://github.com/md2k/claude-token-counter.git
cd claude-token-counter

# Build the daemon
make build
# or: go build -o token-tracker main.go

# Install daemon and statusline script to ~/.claude directory
mkdir -p ~/.claude
cp token-tracker ~/.claude/
cp statusline.mjs ~/.claude/
chmod +x ~/.claude/statusline.mjs ~/.claude/token-tracker
```

### Makefile Targets

```bash
make help     # Show available commands
make build    # Build the binary only
make install  # Build and install to ~/.claude
make clean    # Remove built binary
```

### Configure Claude Code Settings

Edit `~/.claude/settings.json` and add:

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline.mjs"
  }
}
```

Or use the example file:
```bash
cat settings.example.json >> ~/.claude/settings.json
```

## Usage

The daemon **auto-starts** when needed by the statusline script. You don't need to start it manually.

### Manual Daemon Control

```bash
# Start with default settings (auto-started by statusline)
~/.claude/token-tracker

# Custom configuration
~/.claude/token-tracker --port 8888 --timeout 10m --idle-timeout 30m

# Never auto-stop sessions
~/.claude/token-tracker --timeout never

# Never shutdown daemon when idle
~/.claude/token-tracker --idle-timeout never

# Run in background with custom settings
~/.claude/token-tracker --log-level silent > /dev/null 2>&1 &

# Graceful shutdown via API
curl http://localhost:7777/shutdown
```

### Command-line Options

```
--port <port>                  HTTP server port (default: 7777)
--timeout <duration>           Session inactivity timeout (default: 5m)
                              Examples: 5m, 1h, never
--idle-timeout <duration>      Daemon idle shutdown timeout (default: 10m)
                              Daemon stops if no requests for this duration
                              Examples: 10m, 1h, never
--cache-rebuild-alert <duration> Duration to show cache rebuild alert (default: 60s)
                              Shows 🔄 icon for this duration after cache invalidation
                              Examples: 30s, 60s, 90s
--cache-drop-threshold <tokens> Token count drop to detect cache invalidation (default: 10000)
                              Detects checkpoint-based cache expiration
--log-level <level>            Log level: info, silent (default: info)
--pid-file <path>              PID file path (default: ~/.claude/token-tracker.pid)
```

## API

### Endpoints

- **`GET /health`** - Health check
  ```bash
  curl http://localhost:7777/health
  # {"status":"ok"}
  ```

- **`GET /tokens?path=<file>`** - Get token counts for a transcript file
  ```bash
  curl "http://localhost:7777/tokens?path=/path/to/transcript.jsonl"
  # {
  #   "input_tokens": 27814,
  #   "output_tokens": 70997,
  #   "cache_read_tokens": 17891893,
  #   "cache_create_tokens": 1582021,
  #   "last_cache_create_tokens": 1523,
  #   "cache_rebuilding": false
  # }
  ```

  Response fields:
  - `input_tokens`: Fresh input tokens (not from cache)
  - `output_tokens`: Claude's response tokens
  - `cache_read_tokens`: Tokens retrieved from cache (savings)
  - `cache_create_tokens`: Total cumulative tokens written to cache
  - `last_cache_create_tokens`: Tokens written in the most recent message
  - `cache_rebuilding`: Boolean indicating if cache is currently rebuilding
  - `cache_last_read_timestamp`: Unix timestamp of last cache read (0 if no active cache)

- **`GET /status`** - Daemon status and active sessions
  ```bash
  curl http://localhost:7777/status | jq
  # {
  #   "active_sessions": 1,
  #   "timeout": "5m0s",
  #   "sessions": [...]
  # }
  ```

- **`GET /metrics`** - Detailed tracking metrics
  ```bash
  curl http://localhost:7777/metrics | jq
  # {
  #   "active_sessions": 1,
  #   "session_timeout": "5m0s",
  #   "idle_timeout": "10m0s",
  #   "last_request": "2025-10-17T15:42:00+02:00",
  #   "idle_for": "2s",
  #   "sessions": [
  #     {
  #       "path": "/path/to/transcript.jsonl",
  #       "file_size": 819200,
  #       "parse_count": 42,
  #       "avg_parse_time": "1.2ms",
  #       "tracking_duration": "5m30s",
  #       ...
  #     }
  #   ]
  # }
  ```

- **`POST /shutdown`** - Graceful shutdown
  ```bash
  curl http://localhost:7777/shutdown
  # {"status":"shutting down"}
  ```

## Customization

You can customize which sections appear in your statusline by editing `~/.claude/statusline.mjs`. At the top of the file, you'll find the Display Configuration section:

```javascript
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
const SHOW_CACHE_TTL = false;         // Cache TTL countdown timer (daemon only)
                                      // DISABLED: Claude Code v1.0.89+ removed statusline auto-refresh,
                                      // so countdown timer only updates on interaction (not useful)
const SHOW_LINES = true;              // Lines added/removed
const SHOW_200K_WARNING = true;       // Warning when exceeding 200k tokens

// Path Truncation Configuration
const PATH_MAX_LENGTH = 40;           // Maximum path length before truncation
const PATH_SHORTEN_STRATEGY = true;   // Enable intelligent path shortening

// Cache TTL Configuration
const CACHE_TTL_YELLOW = 120;         // Turn yellow at 2 minutes
const CACHE_TTL_RED = 45;             // Turn red at 45 seconds
```

Simply set any option to `false` to hide that section from your statusline.

### Path Truncation

The statusline intelligently truncates long directory paths (inspired by Powerlevel10k):

**Strategy:**
- Keeps project/workspace name full
- Truncates parent directories to first letter
- Keeps current directory (last segment) full
- If still too long, truncates project name with ellipsis

**Examples:**
```
~/git/user/claude-token-counter                 → ~/git/user/claude-token-counter
~/git/user/claude-token-counter/src/components  → ~/g/u/claude-token-counter/s/components
~/git/user/my-very-long-project-name/src/...    → ~/g/u/my-very-long-project-…/s/c/buttons
```

**Configuration:**
- `PATH_MAX_LENGTH`: Maximum characters (default: 40)
- `PATH_SHORTEN_STRATEGY`: Enable/disable truncation (default: true)

### Example: Minimal Statusline

```javascript
const SHOW_MODEL = false;
const SHOW_PATH = false;
const SHOW_GIT = false;
const SHOW_STYLE = false;
const SHOW_COST = false;
const SHOW_DURATION = false;
const SHOW_TOKENS_INPUT_OUTPUT = true;  // Only show tokens
const SHOW_CACHE_READ = true;
const SHOW_CACHE_WRITE = false;
const SHOW_LINES = false;
const SHOW_200K_WARNING = true;
```

This would display only: `27.8k↓ 71.0k↑ᵈ │ ⚡151.3k (99.92%)ᵈ`

## Statusline Indicators

The statusline displays different indicators based on the data source:

- **`27.8k↓ 71.9k↑ᵈ`** - Using daemon (fast, cached) - indicated by superscript `ᵈ`
- **`27.8k↓ 71.9k↑ᶠ`** - Using fallback (direct file parsing) - indicated by superscript `ᶠ`
- **`27.8k↓ 71.9k↑ᵈ 🔄`** - Cache rebuilding after invalidation (shown for 60s by default)
- **`⏳ starting...`** - Daemon is starting up
- **`[tokens N/A]`** - Token data unavailable

The `ᵈ` or `ᶠ` indicator appears after each metric (input/output, cache read, cache write) to clearly show the data source.

### Cache Indicators

When prompt caching is active, the statusline shows cache statistics:

- **`⚡151.3k (99.92%)ᵈ`** - Cache read tokens and efficiency percentage
- **`🗂  +1.5k/150kᵈ`** - Last cache write / Total cache written (daemon mode)
  - `+1.5k` = tokens written in the last message
  - `150k` = cumulative total tokens written to cache
- **`🗂  150kᶠ`** - Total cache written only (fallback mode)
- **Full example**: `27.8k↓ 71.0k↑ᵈ │ ⚡151.3k (99.92%)ᵈ 🗂  +1.5k/150kᵈ`

Cache efficiency is calculated as: `cache_read / (input + cache_read) * 100`

The 🗂 indicator shows `cache_creation_input_tokens`:
- **Last write** (`+1.5k`) - how much cache was created in the most recent message (daemon mode only)
- **Total** (`150k`) - cumulative cache creation for the entire session

**Note**: Fallback mode (ᶠ) shows only total cache write (`🗂  150kᶠ`) for better performance. Daemon mode (ᵈ) shows detailed breakdown (`🗂  +1.5k/150kᵈ`).

### Cache TTL Countdown

**⚠️ DISABLED (as of Claude Code v1.0.89+)**: This feature is currently disabled because Claude Code removed statusline auto-refresh. The countdown timer only updates during interaction, making it not useful as a real-time timer.

The implementation remains in the code and can be re-enabled by setting `SHOW_CACHE_TTL = true` if auto-refresh is restored in future versions.

<details>
<summary>Feature Documentation (for reference)</summary>

When enabled and cache is active in daemon mode, shows time remaining until expiration:

- **`⏱ 4m30sᵈ`** - Green: More than 2 minutes remaining (safe)
- **`⏱ 1m30sᵈ`** - Yellow: 45 seconds to 2 minutes remaining (warning)
- **`⏱ 30sᵈ`** - Red: Less than 45 seconds remaining (critical)
- **Full example**: `27.8k↓ 71.0k↑ᵈ │ ⚡151.3k (99.92%)ᵈ 🗂  +1.5k/150kᵈ ⏱ 3m45sᵈ`

Claude's prompt cache has a 5-minute TTL that refreshes when the cache is **read** (when you send a message and Claude responds using cached context). The daemon tracks the timestamp of the last cache read, and the statusline calculates the remaining time client-side on each refresh. The countdown uses 4.5 minutes (270 seconds) as a safety margin to account for latency.

**Configuration:**
- `SHOW_CACHE_TTL`: Enable/disable TTL countdown display (default: false)
- `CACHE_TTL_YELLOW`: Seconds threshold for yellow warning (default: 120)
- `CACHE_TTL_RED`: Seconds threshold for red critical alert (default: 45)

</details>

## Font Requirements

The statusline uses special Unicode characters including superscripts. For best display, install a Nerd Font:

**Recommended: MesloLGS NF** (used with Powerlevel10k)
- Download: https://github.com/romkatv/powerlevel10k#meslo-nerd-font-patched-for-powerlevel10k
- Or install from [Nerd Fonts](https://www.nerdfonts.com/)

Configure your terminal to use the installed Nerd Font.

## How It Works

1. **Auto-start**: Statusline script checks if daemon is running, starts it if needed
2. **File watching**: Daemon uses `fsnotify` to watch transcript files for changes
3. **Incremental parsing**: Only new lines since last read are parsed
4. **In-memory cache**: Token counts stored in memory for instant API responses
5. **Cache invalidation detection**: Monitors cache_read drops to detect checkpoint-based expiration
   - Claude's prompt cache has a 5-minute TTL
   - Cache expires in checkpoints (older segments first, newer remain)
   - Example: cache_read drops from 151k → 12k (139k expired)
   - Shows 🔄 icon for 60 seconds (configurable) after detection
6. **Auto-cleanup**: Inactive sessions cleaned up after timeout (default: 5m)
7. **Idle shutdown**: Daemon gracefully stops after idle timeout (default: 10m)
8. **Graceful fallback**: If daemon unavailable, statusline falls back to direct file parsing

## Performance

- **Daemon mode**: ~75ms statusline update (includes curl overhead)
- **API response time**: ~1-2ms (cached data)
- **Fallback mode**: ~177ms (direct file parsing)
- **Memory usage**: Minimal (~few MB for typical usage)

## Troubleshooting

### Daemon not starting

```bash
# Check if port is already in use
lsof -i :7777

# Check logs if running manually
~/.claude/token-tracker --log-level info

# Remove stale PID file
rm ~/.claude/token-tracker.pid
```

### Statusline not showing tokens

```bash
# Test statusline manually
echo '{"model":{"display_name":"Test"},"transcript_path":"/path/to/transcript.jsonl","cost":{},"workspace":{"current_dir":"~"}}' | ~/.claude/statusline.mjs

# Check if daemon is running
curl http://localhost:7777/health

# Check daemon status
curl http://localhost:7777/status | jq
```

### View detailed metrics

```bash
# Get comprehensive tracking statistics
curl http://localhost:7777/metrics | jq

# Monitor idle timeout
watch -n 1 'curl -s http://localhost:7777/metrics | jq ".idle_for"'
```

## Transcript Analyzer

The `analyze_transcript.py` script provides detailed analysis of Claude Code transcript files, showing per-message token usage, cache events, and cost breakdowns.

### Features

- **Per-message token tracking** - Single-line table format showing usage per message
- **Cache event detection** - Identifies cache start, reads, invalidations, and growth
- **Per-model breakdown** - Tracks usage across different models (Sonnet, Haiku, Opus)
- **Cost calculation** - Accurate pricing with 5-minute cache TTL rates
- **Cache efficiency** - Shows savings from prompt caching
- **Mixed-model support** - Handles sessions using multiple models

### Usage

```bash
# Basic usage
python3 analyze_transcript.py ~/.claude/transcripts/session-123.jsonl

# Or using the installed script
./analyze_transcript.py ~/.claude/transcripts/session-123.jsonl
```

### Example Output

```
Analyzing: ~/.claude/transcripts/session-123.jsonl

 Msg#    Input   Output   CacheR   CacheC      Ctx    Eff% Event
==================================================================================================================================
    1      552      216        0        0      552       -
    2        4        1        0    15.6k      556       - 🆕 CACHE START
    3        8      423    15.6k    16.1k      564   99.95 ⚡ CACHE READ
  ...
  804        8        6   109.0k   109.8k    31.7k   99.99
  805       10        6        0   109.8k   31.7k       - 🔄 INVALIDATION (↓109.0k)
  ...
==================================================================================================================================

SUMMARY:
Total Messages: 1172

Input Tokens:
  Fresh (non-cached): 37.5k
  From Cache:         146.7m
  TOTAL INPUT:        146.7m

Output Tokens:        97.2k
Cache Written:        1.6m

Note: 'Ctx' column shows cumulative fresh input tokens (running total)

Cache Efficiency: 99.97%

==================================================================================================================================

PER-MODEL BREAKDOWN & COSTS:

claude-3-5-haiku-20250110:
  Messages: 368
  Input:    298.4k
  Output:   5.0k
  Cache R:  0
  Cache W:  2.1k
  Cost:     $0.2609

claude-sonnet-4-5-20250929:
  Messages: 804
  Input:    2.0k
  Output:   88.9k
  Cache R:  14.6m
  Cache W:  1.4m
  Cost:     $10.97

TOTAL COST:         $11.23
=                   $11.23

Cost without cache:  $54.76
Savings from cache:  $43.53 (79.5%)
```

### Column Descriptions

- **Msg#** - Message number in session
- **Input** - Fresh input tokens (non-cached)
- **Output** - Output tokens from Claude's response
- **CacheR** - Cache read tokens (retrieved from cache)
- **CacheC** - Cache creation tokens (written to cache)
- **Ctx** - Cumulative fresh input tokens
- **Eff%** - Cache efficiency for this message
- **Event** - Cache events (🆕 START, ⚡ READ, 🔄 INVALIDATION, 📈 GROWTH)

### Use Cases

- **Debug cache invalidation** - See exactly when and why cache expires
- **Analyze token usage patterns** - Understand how tokens accumulate
- **Cost tracking** - Compare actual costs vs without caching
- **Mixed-model analysis** - Track usage across Haiku and Sonnet
- **Session auditing** - Review complete token usage history

## Integration

The statusline script integrates seamlessly with Claude Code:
- Updates every 300ms (Claude Code refresh rate)
- Auto-starts daemon on first request
- Graceful fallback ensures tokens always display
- Daemon auto-stops when Claude sessions end

## License

MIT

## Contributing

Contributions welcome! Please open an issue or pull request.

## Acknowledgments

- Built for [Claude Code](https://github.com/anthropics/claude-code)
- Font icons from [Nerd Fonts](https://www.nerdfonts.com/)
- Inspired by [Powerlevel10k](https://github.com/romkatv/powerlevel10k)
