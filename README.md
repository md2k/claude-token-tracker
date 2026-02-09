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
--max-scan-buffer <MB>        Max scanner buffer size in MB for parsing large JSONL lines
                              (default: 100). Handles transcript lines containing browser
                              snapshots, base64 screenshots, or large tool results.
                              Starts at 64KB and grows on demand up to this limit.
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
  #   "cache_tier_5m_tokens": 1580000,
  #   "cache_tier_1h_tokens": 2021,
  #   "web_search_count": 3,
  #   "web_fetch_count": 2,
  #   "last_cache_create_tokens": 1523,
  #   "cache_rebuilding": false,
  #   "cache_last_read_timestamp": 1770640116
  # }
  ```

  Response fields:
  - `input_tokens`: Fresh input tokens (not from cache)
  - `output_tokens`: Claude's response tokens
  - `cache_read_tokens`: Tokens retrieved from cache (savings)
  - `cache_create_tokens`: Total cumulative tokens written to cache
  - `cache_tier_5m_tokens`: Tokens written to the 5-minute ephemeral cache tier
  - `cache_tier_1h_tokens`: Tokens written to the 1-hour ephemeral cache tier
  - `web_search_count`: Cumulative web search requests made by server tools
  - `web_fetch_count`: Cumulative web fetch requests made by server tools
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

## Statusline Layout

The statusline uses a 2-line layout:

**Line 1 — Identity, Context, Cost & Location**
```
Sonnet 4.5 │ 200k ctx │ 🤖 agent │ $0.4521 │ 45m12s (API: 12m08s) │ 01fe6720-...81f9016365a7 │ ~/g/F/a/claude-chatplace │  main ✚2
```

**Line 2 — Progress Bar, Tokens & Metrics**
```
▓▓▓▓▓▓░░░░░░░░░░░░░░ 28% │ 676↓ 21k↑ᵈ │ ⚡9.0m (99.99%)ᵈ 🗂 ⚡1.1m(5m) 0(1h)ᵈ │ 🔍3 📥2 │ +156 -23
```

### Progress Bar Colors

The progress bar uses color-coded 20% bands based on context window usage:
- **0-19%**: Cyan — plenty of room
- **20-39%**: Green — comfortable
- **40-59%**: Yellow — moderate usage
- **60-79%**: Orange — getting full
- **80-100%**: Red — nearly full

### New Fields

- **Context window size** (`200k ctx`): from `context_window.context_window_size`
- **API duration** (`API: 12m08s`): from `cost.total_api_duration_ms`
- **Session ID** (truncated): from `session_id`
- **Agent name** (`🤖 agent`): from `agent.name` (shown only when present)
- **Cache tier breakdown** (`🗂 ⚡1.1m(5m) 0(1h)ᵈ`): ephemeral 5-minute and 1-hour cache tiers
- **Web search/fetch** (`🔍3 📥2`): cumulative web search and fetch request counts

## Customization

You can customize the statusline by editing `~/.claude/statusline.mjs`. Configuration options at the top of the file:

```javascript
// Path Truncation Configuration
const PATH_MAX_LENGTH = 40;           // Maximum path length before truncation
const PATH_SHORTEN_STRATEGY = true;   // Enable intelligent path shortening

// Progress Bar Configuration
const BAR_WIDTH = 20;                 // Width of the context window progress bar
```

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

### Cache Tier Breakdown

The `🗂` indicator now shows cache creation broken down by TTL tier:
- **`⚡1.1m(5m)`** — tokens in the 5-minute ephemeral cache
- **`0(1h)`** — tokens in the 1-hour ephemeral cache

### Web Search/Fetch

When Claude uses server-side web tools, counts are shown:
- **`🔍3`** — 3 web search requests
- **`📥2`** — 2 web fetch requests

## Statusline Indicators

The statusline displays different indicators based on the data source:

- **`27.8k↓ 71.9k↑ᵈ`** - Using daemon (fast, cached) - indicated by superscript `ᵈ`
- **`27.8k↓ 71.9k↑ᶠ`** - Using fallback (direct file parsing) - indicated by superscript `ᶠ`
- **`27.8k↓ 71.9k↑ᵈ 🔄`** - Cache rebuilding after invalidation (shown for 60s by default)
- **`⏳ starting...`** - Daemon is starting up
- **`[tokens N/A]`** - Token data unavailable

The `ᵈ` or `ᶠ` indicator appears after each metric (input/output, cache read, cache write) to clearly show the data source.

### Cache Indicators

- **`⚡9.0m (99.99%)ᵈ`** - Cache read tokens and efficiency percentage
- **`🗂 ⚡1.1m(5m) 0(1h)ᵈ`** - Cache creation by tier (5-minute and 1-hour ephemeral)

Cache efficiency is calculated as: `cache_read / (input + cache_read) * 100`

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

### Daemon returns stale/frozen data

If token counts stop updating mid-session, it may be caused by a large JSONL line exceeding the scanner buffer (e.g., browser snapshots, base64 screenshots). Check the daemon logs or test directly:

```bash
# Check for scanner errors
curl "http://localhost:7777/tokens?path=/path/to/transcript.jsonl"
# If you see "bufio.Scanner: token too long", increase the buffer:
~/.claude/token-tracker --max-scan-buffer 200  # 200MB max
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
