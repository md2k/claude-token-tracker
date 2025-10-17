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
}

// SessionTracker tracks a single transcript file
type SessionTracker struct {
	path                 string
	lastSize             int64
	lastModTime          time.Time
	lastAccess           time.Time
	startTime            time.Time
	usage                TokenUsage
	mu                   sync.RWMutex
	watcher              *fsnotify.Watcher
	stopChan             chan struct{}
	stopped              bool
	parseCount           int64
	totalParseTime       time.Duration
	cacheInvalidatedAt   time.Time
	lastCacheReadTokens  int64
	lastCacheCreateTokens int64
}

// Config holds daemon configuration
type Config struct {
	Port                      int
	Timeout                   time.Duration
	IdleTimeout               time.Duration
	CacheRebuildAlertDuration time.Duration
	CacheDropThreshold        int64
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
		CacheDropThreshold:        *cacheDropThreshold,
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
	tracker.mu.RUnlock()

	// Check if cache is currently rebuilding
	cacheRebuilding := !cacheInvalidatedAt.IsZero() &&
		time.Since(cacheInvalidatedAt) < daemon.config.CacheRebuildAlertDuration

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"input_tokens":            usage.InputTokens,
		"output_tokens":           usage.OutputTokens,
		"cache_read_tokens":       usage.CacheReadTokens,
		"cache_create_tokens":     usage.CacheCreateTokens,
		"last_cache_create_tokens": lastCacheCreate,
		"cache_rebuilding":        cacheRebuilding,
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
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"usage"`) {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
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

		// Track the last individual cache create value (not cumulative)
		t.lastCacheCreateTokens = cacheCreate

		// Detect cache invalidation via large drop in cache_read
		// This handles checkpoint-based cache expiration where segments expire gradually
		if t.lastCacheReadTokens > 0 && cacheRead < t.lastCacheReadTokens {
			drop := t.lastCacheReadTokens - cacheRead
			if drop >= daemon.config.CacheDropThreshold {
				t.cacheInvalidatedAt = time.Now()
				logger.Printf("Cache invalidation detected for %s: %d tokens dropped (was %d, now %d)",
					t.path, drop, t.lastCacheReadTokens, cacheRead)
			}
		}

		// Update last cache read value for next comparison
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
