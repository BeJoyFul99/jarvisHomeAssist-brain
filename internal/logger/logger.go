package logger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is a single structured log line persisted to disk.
type Entry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

// subscriber receives new log entries in real time.
type subscriber struct {
	ch chan Entry
}

// Logger writes structured JSON log lines to a file and supports
// real-time streaming via subscribers.
type Logger struct {
	mu          sync.Mutex
	file        *os.File
	writer      *bufio.Writer
	filePath    string
	subMu       sync.RWMutex
	subscribers map[*subscriber]struct{}
}

// New creates (or opens) a log file and returns a Logger.
// The directory is created if it does not exist.
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	name := time.Now().Format("2006-01-02") + ".jsonl"
	path := filepath.Join(dir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	l := &Logger{
		file:        f,
		writer:      bufio.NewWriter(f),
		filePath:    path,
		subscribers: make(map[*subscriber]struct{}),
	}

	// Also copy structured entries to Go's default logger (stdout).
	log.SetOutput(io.MultiWriter(os.Stdout))

	return l, nil
}

// Write persists an entry and broadcasts it to live subscribers.
func (l *Logger) Write(level, component, message string) {
	entry := Entry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Level:     level,
		Component: component,
		Message:   message,
	}

	raw, _ := json.Marshal(entry)

	l.mu.Lock()
	l.writer.Write(raw)
	l.writer.WriteByte('\n')
	l.writer.Flush()
	l.mu.Unlock()

	// Stdout mirror
	log.Printf("[%s] [%s] %s", level, component, message)

	// Broadcast to subscribers
	l.subMu.RLock()
	for s := range l.subscribers {
		select {
		case s.ch <- entry:
		default: // slow consumer, drop
		}
	}
	l.subMu.RUnlock()
}

// Convenience helpers
func (l *Logger) Info(component, msg string)  { l.Write("info", component, msg) }
func (l *Logger) Warn(component, msg string)  { l.Write("warn", component, msg) }
func (l *Logger) Error(component, msg string) { l.Write("error", component, msg) }
func (l *Logger) Debug(component, msg string) { l.Write("debug", component, msg) }

// Subscribe returns a channel of live entries and an unsubscribe func.
func (l *Logger) Subscribe() (<-chan Entry, func()) {
	s := &subscriber{ch: make(chan Entry, 128)}
	l.subMu.Lock()
	l.subscribers[s] = struct{}{}
	l.subMu.Unlock()

	return s.ch, func() {
		l.subMu.Lock()
		delete(l.subscribers, s)
		close(s.ch)
		l.subMu.Unlock()
	}
}

// Page holds a paginated slice of log entries.
type Page struct {
	Entries    []Entry `json:"entries"`
	Total      int     `json:"total"`
	Page       int     `json:"page"`
	PerPage    int     `json:"per_page"`
	TotalPages int     `json:"total_pages"`
}

// ReadPage reads entries from the current log file with pagination.
// page is 1-based. Entries are returned newest-first.
func (l *Logger) ReadPage(page, perPage int) (Page, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 500 {
		perPage = 100
	}

	l.mu.Lock()
	l.writer.Flush()
	l.mu.Unlock()

	f, err := os.Open(l.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return Page{Page: page, PerPage: perPage}, nil
		}
		return Page{}, err
	}
	defer f.Close()

	// Read all lines (log files are typically manageable in size for a day)
	var all []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}

	total := len(all)
	totalPages := (total + perPage - 1) / perPage

	// Reverse so newest is first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	start := (page - 1) * perPage
	if start >= total {
		return Page{Entries: []Entry{}, Total: total, Page: page, PerPage: perPage, TotalPages: totalPages}, nil
	}
	end := start + perPage
	if end > total {
		end = total
	}

	return Page{
		Entries:    all[start:end],
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
	}, nil
}

// FilePath returns the current log file path.
func (l *Logger) FilePath() string { return l.filePath }

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writer.Flush()
	return l.file.Close()
}
