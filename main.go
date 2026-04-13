package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TokenUsage holds cumulative token counts for a session
type TokenUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	CacheTier5mTokens int64 `json:"cache_tier_5m_tokens"`
	CacheTier1hTokens int64 `json:"cache_tier_1h_tokens"`
	WebSearchCount    int64 `json:"web_search_count"`
	WebFetchCount     int64 `json:"web_fetch_count"`
}

// SessionTracker tracks a single transcript file
type SessionTracker struct {
	path                  string
	lastSize              int64
	lastModTime           time.Time
	lastAccess            time.Time
	startTime             time.Time
	usage                 TokenUsage
	mu                    sync.RWMutex
	watcher               *fsnotify.Watcher
	stopChan              chan struct{}
	stopped               bool
	parseCount            int64
	totalParseTime        time.Duration
	cacheInvalidatedAt    time.Time
	lastCacheReadTokens    int64
	lastCacheCreateTokens  int64
	lastCacheTier5mTokens  int64
	lastCacheTier1hTokens  int64
	lastCacheReadTime      time.Time
	lastCacheEvent         string
	prevCacheReadTokens    int64
	prevCacheCreateTokens  int64
	invalidationCount      int64
	totalTokensInvalidated int64
}

// Config holds daemon configuration
type Config struct {
	Port                      int
	Timeout                   time.Duration
	IdleTimeout               time.Duration
	CacheRebuildAlertDuration time.Duration
	CacheTTLOffset            time.Duration
	CacheDropThreshold        int64
	MaxScanBufferSize         int
	LogLevel                  string
	PIDFile                   string
	NeverTimeout              bool
	NeverIdleStop             bool
}

// Daemon manages all session trackers
type Daemon struct {
	config       Config
	sessions     map[string]*SessionTracker
	mu           sync.RWMutex
	cleanupCh    chan struct{}
	lastRequest  time.Time
	requestMu    sync.RWMutex
}

var (
	daemon *Daemon
	logger *log.Logger
)

func main() {
	// Parse command-line flags
	config := parseFlags()

	// Setup logger
	logger = log.New(os.Stdout, "", log.LstdFlags)
	if config.LogLevel == "silent" {
		logger.SetOutput(os.NewFile(0, os.DevNull))
	}

	// Write PID file
	if err := writePIDFile(config.PIDFile); err != nil {
		logger.Fatalf("Failed to write PID file: %v", err)
	}
	defer os.Remove(config.PIDFile)

	// Initialize daemon
	daemon = &Daemon{
		config:      config,
		sessions:    make(map[string]*SessionTracker),
		cleanupCh:   make(chan struct{}),
		lastRequest: time.Now(),
	}

	// Start cleanup goroutine if timeout is enabled
	if !config.NeverTimeout {
		go daemon.cleanupLoop()
	}

	// Start idle shutdown goroutine if enabled
	if !config.NeverIdleStop {
		go daemon.idleShutdownLoop()
	}

	// Setup HTTP server
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/tokens", tokensHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/metrics", metricsHandler)
	http.HandleFunc("/shutdown", shutdownHandler)

	addr := fmt.Sprintf(":%d", config.Port)
	logger.Printf("Token tracker daemon starting on %s (timeout: %v)", addr, formatTimeout(config))

	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Fatalf("Server failed: %v", err)
	}
}

func parseFlags() Config {
	port := flag.Int("port", 7777, "HTTP server port")
	timeoutStr := flag.String("timeout", "5m", "Session inactivity timeout (e.g., 5m, 1h, or 'never')")
	idleTimeoutStr := flag.String("idle-timeout", "10m", "Daemon idle shutdown timeout (e.g., 10m, 1h, or 'never')")
	cacheRebuildAlertStr := flag.String("cache-rebuild-alert", "60s", "Cache rebuild alert duration (e.g., 30s, 60s, 90s)")
	cacheDropThreshold := flag.Int64("cache-drop-threshold", 10000, "Cache drop threshold in tokens to detect invalidation (default: 10000)")
	maxScanBuffer := flag.Int("max-scan-buffer", 100, "Max scanner buffer size in MB for parsing large JSONL lines (default: 100)")
	cacheTTLOffsetStr := flag.String("cache-ttl-offset", "10s", "Safety margin subtracted from cache TTL countdown (e.g., 10s, 30s)")
	logLevel := flag.String("log-level", "info", "Log level (info, silent)")
	pidFile := flag.String("pid-file", "", "PID file path (default: ~/.claude/token-tracker.pid)")

	flag.Parse()

	// Parse session timeout
	var timeout time.Duration
	var neverTimeout bool
	if *timeoutStr == "never" {
		neverTimeout = true
		timeout = 0
	} else {
		var err error
		timeout, err = time.ParseDuration(*timeoutStr)
		if err != nil {
			log.Fatalf("Invalid timeout format: %v", err)
		}
	}

	// Parse idle timeout
	var idleTimeout time.Duration
	var neverIdleStop bool
	if *idleTimeoutStr == "never" {
		neverIdleStop = true
		idleTimeout = 0
	} else {
		var err error
		idleTimeout, err = time.ParseDuration(*idleTimeoutStr)
		if err != nil {
			log.Fatalf("Invalid idle-timeout format: %v", err)
		}
	}

	// Parse cache rebuild alert duration
	cacheRebuildAlert, err := time.ParseDuration(*cacheRebuildAlertStr)
	if err != nil {
		log.Fatalf("Invalid cache-rebuild-alert format: %v", err)
	}

	// Parse cache TTL offset
	cacheTTLOffset, err := time.ParseDuration(*cacheTTLOffsetStr)
	if err != nil {
		log.Fatalf("Invalid cache-ttl-offset format: %v", err)
	}

	// Determine PID file path
	pidPath := *pidFile
	if pidPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Cannot determine home directory: %v", err)
		}
		pidPath = filepath.Join(home, ".claude", "token-tracker.pid")
	}

	return Config{
		Port:                      *port,
		Timeout:                   timeout,
		IdleTimeout:               idleTimeout,
		CacheRebuildAlertDuration: cacheRebuildAlert,
		CacheTTLOffset:            cacheTTLOffset,
		CacheDropThreshold:        *cacheDropThreshold,
		MaxScanBufferSize:         *maxScanBuffer * 1024 * 1024,
		LogLevel:                  *logLevel,
		PIDFile:                   pidPath,
		NeverTimeout:              neverTimeout,
		NeverIdleStop:             neverIdleStop,
	}
}

func formatTimeout(config Config) string {
	if config.NeverTimeout {
		return "never"
	}
	return config.Timeout.String()
}

func writePIDFile(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Check if already running
	if data, err := os.ReadFile(path); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if processExists(pid) {
			return fmt.Errorf("daemon already running (PID %d)", pid)
		}
		// Stale PID file, remove it
		logger.Printf("Removing stale PID file (PID %d no longer exists)", pid)
		os.Remove(path)
	}

	// Write current PID
	pid := os.Getpid()
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0644)
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(os.Signal(nil))
	return err == nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	daemon.updateLastRequest()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func shutdownHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})

	logger.Printf("Shutdown requested via API")

	// Stop all session trackers and exit gracefully
	go func() {
		daemon.mu.Lock()
		for path, tracker := range daemon.sessions {
			tracker.stop()
			logger.Printf("Stopped tracking: %s", path)
		}
		daemon.mu.Unlock()

		// Give time for response to be sent
		time.Sleep(100 * time.Millisecond)

		// Exit gracefully (defer will clean up PID file)
		os.Exit(0)
	}()
}

func tokensHandler(w http.ResponseWriter, r *http.Request) {
	daemon.updateLastRequest()

	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Missing 'path' parameter", http.StatusBadRequest)
		return
	}

	// Get or create session tracker
	tracker, err := daemon.getOrCreateTracker(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to track file: %v", err), http.StatusInternalServerError)
		return
	}

	// Update last access time
	tracker.mu.Lock()
	tracker.lastAccess = time.Now()
	tracker.mu.Unlock()

	// Return current usage with cache rebuilding status
	tracker.mu.RLock()
	usage := tracker.usage
	cacheInvalidatedAt := tracker.cacheInvalidatedAt
	lastCacheCreate := tracker.lastCacheCreateTokens
	lastTier5m := tracker.lastCacheTier5mTokens
	lastTier1h := tracker.lastCacheTier1hTokens
	lastCacheReadTime := tracker.lastCacheReadTime
	cacheEvent := tracker.lastCacheEvent
	invalidationCount := tracker.invalidationCount
	totalTokensInvalidated := tracker.totalTokensInvalidated
	tracker.mu.RUnlock()

	// Check if cache is currently rebuilding
	cacheRebuilding := !cacheInvalidatedAt.IsZero() &&
		time.Since(cacheInvalidatedAt) < daemon.config.CacheRebuildAlertDuration

	// Return timestamp of last cache read for client-side countdown calculation
	// TTL is refreshed when cache is read (when user sends message and Claude responds)
	var cacheLastReadTimestamp int64 = 0
	if !lastCacheReadTime.IsZero() {
		cacheLastReadTimestamp = lastCacheReadTime.Unix()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"input_tokens":              usage.InputTokens,
		"output_tokens":             usage.OutputTokens,
		"cache_read_tokens":         usage.CacheReadTokens,
		"cache_create_tokens":       usage.CacheCreateTokens,
		"cache_tier_5m_tokens":      usage.CacheTier5mTokens,
		"cache_tier_1h_tokens":      usage.CacheTier1hTokens,
		"web_search_count":          usage.WebSearchCount,
		"web_fetch_count":           usage.WebFetchCount,
		"last_cache_create_tokens":    lastCacheCreate,
		"last_cache_tier_5m_tokens":  lastTier5m,
		"last_cache_tier_1h_tokens":  lastTier1h,
		"cache_event":                cacheEvent,
		"cache_rebuilding":           cacheRebuilding,
		"cache_last_read_timestamp":  cacheLastReadTimestamp,
		"cache_ttl_offset_seconds":   int(daemon.config.CacheTTLOffset.Seconds()),
		"invalidation_count":         invalidationCount,
		"total_tokens_invalidated":   totalTokensInvalidated,
	})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	daemon.updateLastRequest()

	daemon.mu.RLock()
	sessionCount := len(daemon.sessions)
	sessions := make([]map[string]interface{}, 0, sessionCount)

	for path, tracker := range daemon.sessions {
		tracker.mu.RLock()
		sessions = append(sessions, map[string]interface{}{
			"path":        path,
			"last_access": tracker.lastAccess.Format(time.RFC3339),
			"tokens":      tracker.usage,
		})
		tracker.mu.RUnlock()
	}
	daemon.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"active_sessions": sessionCount,
		"timeout":         formatTimeout(daemon.config),
		"sessions":        sessions,
	})
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	daemon.updateLastRequest()

	daemon.mu.RLock()
	sessionCount := len(daemon.sessions)
	sessions := make([]map[string]interface{}, 0, sessionCount)

	for path, tracker := range daemon.sessions {
		tracker.mu.RLock()

		avgParseTime := time.Duration(0)
		if tracker.parseCount > 0 {
			avgParseTime = tracker.totalParseTime / time.Duration(tracker.parseCount)
		}

		sessions = append(sessions, map[string]interface{}{
			"path":              path,
			"file_size":         tracker.lastSize,
			"last_modified":     tracker.lastModTime.Format(time.RFC3339),
			"last_access":       tracker.lastAccess.Format(time.RFC3339),
			"tracking_since":    tracker.startTime.Format(time.RFC3339),
			"tracking_duration": time.Since(tracker.startTime).Round(time.Second).String(),
			"parse_count":       tracker.parseCount,
			"total_parse_time":  tracker.totalParseTime.Round(time.Millisecond).String(),
			"avg_parse_time":    avgParseTime.Round(time.Millisecond).String(),
			"tokens":            tracker.usage,
		})
		tracker.mu.RUnlock()
	}
	daemon.mu.RUnlock()

	daemon.requestMu.RLock()
	lastReq := daemon.lastRequest
	daemon.requestMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"active_sessions":     sessionCount,
		"session_timeout":     formatTimeout(daemon.config),
		"idle_timeout":        formatIdleTimeout(daemon.config),
		"last_request":        lastReq.Format(time.RFC3339),
		"idle_for":            time.Since(lastReq).Round(time.Second).String(),
		"sessions":            sessions,
	})
}

func formatTokens(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fm", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatIdleTimeout(config Config) string {
	if config.NeverIdleStop {
		return "never"
	}
	return config.IdleTimeout.String()
}

func (d *Daemon) getOrCreateTracker(path string) (*SessionTracker, error) {
	// Check if tracker already exists
	d.mu.RLock()
	tracker, exists := d.sessions[path]
	d.mu.RUnlock()

	if exists {
		return tracker, nil
	}

	// Create new tracker
	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check (another goroutine might have created it)
	if tracker, exists = d.sessions[path]; exists {
		return tracker, nil
	}

	tracker, err := newSessionTracker(path)
	if err != nil {
		return nil, err
	}

	d.sessions[path] = tracker
	logger.Printf("Started tracking: %s", path)

	return tracker, nil
}

func newSessionTracker(path string) (*SessionTracker, error) {
	// Create file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	now := time.Now()
	tracker := &SessionTracker{
		path:       path,
		lastAccess: now,
		startTime:  now,
		watcher:    watcher,
		stopChan:   make(chan struct{}),
	}

	// Initial parse
	if err := tracker.parseFile(); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	// Start watching
	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to watch file: %w", err)
	}

	go tracker.watchLoop()

	return tracker, nil
}

func (t *SessionTracker) parseFile() error {
	startTime := time.Now()
	defer func() {
		t.mu.Lock()
		t.parseCount++
		t.totalParseTime += time.Since(startTime)
		t.mu.Unlock()
	}()

	info, err := os.Stat(t.path)
	if err != nil {
		return err
	}

	// Skip if file hasn't changed
	if t.lastSize == info.Size() && t.lastModTime.Equal(info.ModTime()) {
		return nil
	}

	file, err := os.Open(t.path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Seek to last read position for incremental parsing
	if t.lastSize > 0 && t.lastSize <= info.Size() {
		if _, err := file.Seek(t.lastSize, 0); err != nil {
			return err
		}
	}

	// Parse new lines
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), daemon.config.MaxScanBufferSize) // start at 64KB, grow up to --max-scan-buffer
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"usage"`) {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		// Extract timestamp from JSONL line (ISO 8601)
		var lineTimestamp time.Time
		if ts, ok := data["timestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				lineTimestamp = parsed
			} else if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				lineTimestamp = parsed
			}
		}
		if lineTimestamp.IsZero() {
			lineTimestamp = time.Now() // fallback if no timestamp in line
		}

		// Extract usage data
		var usage map[string]interface{}
		if msg, ok := data["message"].(map[string]interface{}); ok {
			usage, _ = msg["usage"].(map[string]interface{})
		}
		if usage == nil {
			usage, _ = data["usage"].(map[string]interface{})
		}
		if usage == nil {
			continue
		}

		// Extract token values
		var cacheRead, cacheCreate int64
		if val, ok := usage["cache_read_input_tokens"].(float64); ok {
			cacheRead = int64(val)
		}
		if val, ok := usage["cache_creation_input_tokens"].(float64); ok {
			cacheCreate = int64(val)
		}

		// Extract cache tier breakdown
		var tier5m, tier1h int64
		if cacheCreation, ok := usage["cache_creation"].(map[string]interface{}); ok {
			if val, ok := cacheCreation["ephemeral_5m_input_tokens"].(float64); ok {
				tier5m = int64(val)
			}
			if val, ok := cacheCreation["ephemeral_1h_input_tokens"].(float64); ok {
				tier1h = int64(val)
			}
		}

		// Extract web search/fetch counts
		var webSearch, webFetch int64
		if serverToolUse, ok := usage["server_tool_use"].(map[string]interface{}); ok {
			if val, ok := serverToolUse["web_search_requests"].(float64); ok {
				webSearch = int64(val)
			}
			if val, ok := serverToolUse["web_fetch_requests"].(float64); ok {
				webFetch = int64(val)
			}
		}

		// Update totals first
		t.mu.Lock()
		if val, ok := usage["input_tokens"].(float64); ok {
			t.usage.InputTokens += int64(val)
		}
		if val, ok := usage["output_tokens"].(float64); ok {
			t.usage.OutputTokens += int64(val)
		}

		// Update token counts
		t.usage.CacheReadTokens += cacheRead
		t.usage.CacheCreateTokens += cacheCreate
		t.usage.CacheTier5mTokens += tier5m
		t.usage.CacheTier1hTokens += tier1h
		t.usage.WebSearchCount += webSearch
		t.usage.WebFetchCount += webFetch

		// Track the last individual cache create value (not cumulative)
		t.lastCacheCreateTokens = cacheCreate
		t.lastCacheTier5mTokens = tier5m
		t.lastCacheTier1hTokens = tier1h

		// Detect cache events (same logic as analyze_transcript.py)
		// Priority: INVALIDATION > START > READ > GREW
		if t.prevCacheReadTokens > 0 && cacheRead < t.prevCacheReadTokens {
			drop := t.prevCacheReadTokens - cacheRead
			if drop >= daemon.config.CacheDropThreshold {
				t.cacheInvalidatedAt = lineTimestamp
				t.lastCacheEvent = fmt.Sprintf("🔄 INVALIDATION (↓%s)", formatTokens(drop))
				t.invalidationCount++
				t.totalTokensInvalidated += drop
				logger.Printf("Cache invalidation detected for %s: %d tokens dropped (was %d, now %d)",
					t.path, drop, t.prevCacheReadTokens, cacheRead)
			}
		} else if cacheCreate > 0 && t.prevCacheCreateTokens == 0 {
			t.lastCacheEvent = "🆕 CACHE START"
		} else if cacheRead > 0 && t.prevCacheReadTokens == 0 {
			t.lastCacheEvent = "⚡ CACHE READ"
		} else if cacheRead > t.prevCacheReadTokens && t.prevCacheReadTokens > 0 {
			increase := cacheRead - t.prevCacheReadTokens
			if increase >= 1000 {
				t.lastCacheEvent = fmt.Sprintf("📈 GREW (+%s)", formatTokens(increase))
			}
		}

		// Update previous values for next comparison
		t.prevCacheReadTokens = cacheRead
		t.prevCacheCreateTokens = cacheCreate

		// Track when cache was last read (for TTL countdown)
		// Uses transcript timestamp for accuracy instead of daemon wall-clock
		// Any cache read refreshes the 5m TTL, even if token count unchanged
		if cacheRead > 0 {
			t.lastCacheReadTime = lineTimestamp
		}

		// Update last cache read value
		if cacheRead > 0 {
			t.lastCacheReadTokens = cacheRead
		}

		t.mu.Unlock()
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// Update tracking info
	t.lastSize = info.Size()
	t.lastModTime = info.ModTime()

	return nil
}

func (t *SessionTracker) watchLoop() {
	for {
		select {
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				if err := t.parseFile(); err != nil {
					logger.Printf("Error parsing %s: %v", t.path, err)
				}
			}
		case err, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			logger.Printf("Watcher error for %s: %v", t.path, err)
		case <-t.stopChan:
			return
		}
	}
}

func (t *SessionTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}

	t.stopped = true
	close(t.stopChan)
	t.watcher.Close()
}

func (d *Daemon) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cleanupInactive()
		case <-d.cleanupCh:
			return
		}
	}
}

func (d *Daemon) cleanupInactive() {
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	for path, tracker := range d.sessions {
		tracker.mu.RLock()
		lastAccess := tracker.lastAccess
		tracker.mu.RUnlock()

		if now.Sub(lastAccess) > d.config.Timeout {
			tracker.stop()
			delete(d.sessions, path)
			logger.Printf("Stopped tracking (inactive): %s", path)
		}
	}
}

func (d *Daemon) updateLastRequest() {
	d.requestMu.Lock()
	d.lastRequest = time.Now()
	d.requestMu.Unlock()
}

func (d *Daemon) idleShutdownLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.requestMu.RLock()
			lastReq := d.lastRequest
			d.requestMu.RUnlock()

			if time.Since(lastReq) > d.config.IdleTimeout {
				logger.Printf("No requests for %v, shutting down gracefully", d.config.IdleTimeout)

				// Stop all trackers
				d.mu.Lock()
				for path, tracker := range d.sessions {
					tracker.stop()
					logger.Printf("Stopped tracking: %s", path)
				}
				d.mu.Unlock()

				// Exit (defer will clean up PID file)
				os.Exit(0)
			}
		case <-d.cleanupCh:
			return
		}
	}
}
