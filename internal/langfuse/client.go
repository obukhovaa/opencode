package langfuse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	defaultBaseURL = "https://cloud.langfuse.com"
	flushInterval  = 1 * time.Second
	maxBatchSize   = 10
	ingestionPath  = "/api/public/ingestion"
)

// Client is a Langfuse ingestion client that batches events and flushes
// them periodically or when the batch reaches a threshold.
type Client struct {
	publicKey  string
	secretKey  string
	baseURL    string
	httpClient *http.Client

	mu           sync.Mutex
	queue        []IngestionEvent
	done         chan struct{}
	wg           sync.WaitGroup
	shutdownOnce sync.Once
}

// New creates a new Langfuse client. Keys and baseURL are resolved from
// the provided values, falling back to environment variables.
func New(publicKey, secretKey, baseURL string) *Client {
	pk := resolveKey(publicKey, "LANGFUSE_PUBLIC_KEY")
	sk := resolveKey(secretKey, "LANGFUSE_SECRET_KEY")
	bu := resolveKey(baseURL, "LANGFUSE_BASE_URL")
	if bu == "" {
		bu = defaultBaseURL
	}
	bu = strings.TrimRight(bu, "/")

	c := &Client{
		publicKey:  pk,
		secretKey:  sk,
		baseURL:    bu,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		queue:      make([]IngestionEvent, 0, maxBatchSize),
		done:       make(chan struct{}),
	}
	c.wg.Add(1)
	go c.flushLoop()
	return c
}

// Enabled returns true if the client has valid credentials.
func (c *Client) Enabled() bool {
	return c.publicKey != "" && c.secretKey != ""
}

// TraceCreate enqueues a trace-create event.
func (c *Client) TraceCreate(body TraceBody) {
	c.enqueue(IngestionEvent{
		ID:        uuid.New().String(),
		Type:      EventTraceCreate,
		Timestamp: time.Now().UTC(),
		Body:      body,
	})
}

// GenerationCreate enqueues a generation-create event.
func (c *Client) GenerationCreate(body GenerationBody) {
	c.enqueue(IngestionEvent{
		ID:        uuid.New().String(),
		Type:      EventGenerationCreate,
		Timestamp: time.Now().UTC(),
		Body:      body,
	})
}

// GenerationEnd enqueues a generation-update event with completion data.
func (c *Client) GenerationEnd(body GenerationBody) {
	c.enqueue(IngestionEvent{
		ID:        uuid.New().String(),
		Type:      EventGenerationUpdate,
		Timestamp: time.Now().UTC(),
		Body:      body,
	})
}

// Shutdown flushes all remaining events and stops the background goroutine.
// Blocks until all events are sent or the shutdown timeout is reached.
// Safe to call multiple times.
func (c *Client) Shutdown() {
	c.shutdownOnce.Do(func() {
		close(c.done)
		c.wg.Wait()

		// Final flush of any remaining events
		c.mu.Lock()
		remaining := c.queue
		c.queue = nil
		c.mu.Unlock()

		if len(remaining) > 0 {
			c.sendBatch(remaining)
		}
	})
}

func (c *Client) enqueue(event IngestionEvent) {
	if !c.Enabled() {
		return
	}

	c.mu.Lock()
	c.queue = append(c.queue, event)
	shouldFlush := len(c.queue) >= maxBatchSize
	var batch []IngestionEvent
	if shouldFlush {
		batch = c.queue
		c.queue = make([]IngestionEvent, 0, maxBatchSize)
	}
	c.mu.Unlock()

	if shouldFlush && len(batch) > 0 {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.sendBatch(batch)
		}()
	}
}

func (c *Client) flushLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.flush()
		}
	}
}

func (c *Client) flush() {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}
	batch := c.queue
	c.queue = make([]IngestionEvent, 0, maxBatchSize)
	c.mu.Unlock()

	c.sendBatch(batch)
}

func (c *Client) sendBatch(events []IngestionEvent) {
	if len(events) == 0 {
		return
	}

	body, err := json.Marshal(IngestionRequest{Batch: events})
	if err != nil {
		logging.Warn("langfuse: failed to marshal batch", "error", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+ingestionPath, bytes.NewReader(body))
	if err != nil {
		logging.Warn("langfuse: failed to create request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.publicKey, c.secretKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logging.Warn("langfuse: failed to send batch", "error", err, "events", len(events))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		logging.Warn("langfuse: ingestion returned unexpected status",
			"status", resp.StatusCode, "events", len(events))
		return
	}

	logging.Debug("langfuse: batch sent", "events", len(events), "status", resp.StatusCode)
}

// resolveKey resolves a config value: supports "env:VAR_NAME" syntax,
// falls back to the given environment variable, or returns the raw value.
func resolveKey(value, envFallback string) string {
	if value == "" {
		return os.Getenv(envFallback)
	}
	if after, ok := strings.CutPrefix(value, "env:"); ok {
		return os.Getenv(after)
	}
	return value
}

// Nop returns a no-op client that discards all events.
func Nop() *Client {
	c := &Client{
		done: make(chan struct{}),
	}
	return c
}

var globalClient *Client

// Init creates the global Langfuse client. Should be called once at startup.
// Returns true if the client is enabled (credentials resolved successfully).
func Init(publicKey, secretKey, baseURL string) bool {
	globalClient = New(publicKey, secretKey, baseURL)
	return globalClient.Enabled()
}

// Get returns the global Langfuse client, or nil if not initialized.
func Get() *Client {
	return globalClient
}

// Shutdown shuts down the global client, flushing remaining events.
func ShutdownGlobal() {
	if globalClient != nil {
		globalClient.Shutdown()
	}
}

// FormatGenerationName builds a generation name like "coder/claude-sonnet-4-6".
func FormatGenerationName(agentID, model string) string {
	return fmt.Sprintf("%s/%s", agentID, model)
}
