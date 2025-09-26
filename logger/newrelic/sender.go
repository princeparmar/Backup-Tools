// Copyright (C) 2024 Storj Labs, Inc.
// See LICENSE for copying information.

package newrelic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	maxBuffer     = 500
	flushInterval = 2 * time.Second
	httpTimeout   = 30 * time.Second
	maxRetries    = 3
)

type Sender struct {
	apiKey   string
	enabled  bool
	client   *http.Client
	buffer   []LogEntry
	mu       sync.Mutex
	ticker   *time.Ticker
	shutdown chan struct{}
	wg       sync.WaitGroup
}

type LogEntry struct {
	Timestamp int64                  `json:"timestamp"`
	Message   string                 `json:"message"`
	Level     string                 `json:"level,omitempty"`
	Caller    string                 `json:"caller,omitempty"`
	LogType   string                 `json:"logtype"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

func NewSender(apiKey string, enabled bool) *Sender {
	s := &Sender{
		apiKey:   apiKey,
		enabled:  enabled,
		client:   &http.Client{Timeout: httpTimeout},
		buffer:   make([]LogEntry, 0, maxBuffer),
		shutdown: make(chan struct{}),
	}
	s.ticker = time.NewTicker(flushInterval)
	go s.flushLoop()
	return s
}

func (s *Sender) SendLog(data []byte) {
	if !s.enabled || s.apiKey == "" {
		return
	}
	s.addToBuffer(s.parseLog(data))
}

func (s *Sender) parseLog(data []byte) LogEntry {
	var logData map[string]interface{}
	if err := json.Unmarshal(data, &logData); err != nil {
		return LogEntry{
			Timestamp: time.Now().UnixMilli(),
			Message:   string(data),
			LogType:   "application",
		}
	}

	entry := LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Message:   fmt.Sprintf("%v", logData["M"]),
		Level:     fmt.Sprintf("%v", logData["L"]),
		Caller:    fmt.Sprintf("%v", logData["C"]),
		LogType:   "application",
		Fields:    make(map[string]interface{}),
	}

	// Add extra fields (excluding zap core fields)
	for k, v := range logData {
		if k != "M" && k != "L" && k != "C" && k != "N" && k != "T" && k != "S" {
			entry.Fields[k] = v
		}
	}

	return entry
}

func (s *Sender) addToBuffer(entry LogEntry) {
	s.mu.Lock()
	s.buffer = append(s.buffer, entry)
	if len(s.buffer) >= maxBuffer {
		s.wg.Add(1)
		go s.flushAsync()
	}
	s.mu.Unlock()
}

func (s *Sender) flushLoop() {
	for {
		select {
		case <-s.ticker.C:
			s.flush()
		case <-s.shutdown:
			return
		}
	}
}

func (s *Sender) flush() {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return
	}
	logs := make([]LogEntry, len(s.buffer))
	copy(logs, s.buffer)
	s.buffer = s.buffer[:0]
	s.mu.Unlock()
	s.sendBatch(logs)
}

func (s *Sender) flushAsync() {
	defer s.wg.Done()
	s.flush()
}

func (s *Sender) sendBatch(logs []LogEntry) {
	payload, err := json.Marshal(logs)
	if err != nil {
		return
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("POST", "https://log-api.newrelic.com/log/v1", bytes.NewReader(payload))
		if err != nil {
			time.Sleep(time.Second * time.Duration(attempt))
			continue
		}

		req.Header.Set("Api-Key", s.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.client.Do(req)
		if err != nil {
			time.Sleep(time.Second * time.Duration(attempt))
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
		if resp.StatusCode == 403 {
			return // Auth error, no retry
		}
		time.Sleep(time.Second * time.Duration(attempt))
	}
}

func (s *Sender) Close() {
	s.ticker.Stop()
	close(s.shutdown)
	s.wg.Wait()

	s.mu.Lock()
	if len(s.buffer) > 0 {
		logs := make([]LogEntry, len(s.buffer))
		copy(logs, s.buffer)
		s.mu.Unlock()
		s.sendBatch(logs)
	} else {
		s.mu.Unlock()
	}
}
