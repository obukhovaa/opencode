// Command redaction-e2e drives the real telemetry redaction pipeline end to
// end, out of process, for scripts/test/redaction.sh.
//
// It runs the whole chain that unit tests cannot: a .opencode.json on disk →
// viper → config.Load → the Langfuse client → a real gzipped OTLP export over
// HTTP → protobuf decode of what actually left the process. Unit tests assert
// on in-memory spans; this asserts on bytes on the wire, which is the only way
// to catch a config that survives json.Unmarshal but not the loader.
//
// Usage: redaction-e2e -dir <sandbox with .opencode.json>
// Emits one JSON verdict on stdout.
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
)

// Defused synthetic credentials — see internal/redact/testdata/corpus.md.
const (
	fakePAT      = "glpat-EXAMPLExTbVm2LpR8sJdHy4FcZaXeW6uNi0"
	fakeAWSKey   = "AKIAIOSFODNN7EXAMPLE"
	fakeSlack    = "xoxb-EXAMPLE-EXAMPLE-EXAMPLEf7Gh9Ij1Kl3M"
	fakeCloneURL = "https://oauth2:" + fakePAT + "@gitlab.com/piano/composer/agents/developer.git"
	falsePos     = "sk-clusters-fork-ebs-csi-metrics"
	customSecret = "PI-123456789012"
)

type verdict struct {
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	Attributes  int      `json:"attributes"`
	Markers     []string `json:"markers"`
	LeakedItems []string `json:"leaked,omitempty"`
	FalsePos    bool     `json:"false_positive_preserved"`
}

func main() {
	dir := flag.String("dir", "", "sandbox directory containing .opencode.json")
	flag.Parse()

	v := run(*dir)
	out, _ := json.Marshal(v)
	fmt.Println(string(out))
	if !v.OK {
		os.Exit(1)
	}
}

func run(dir string) verdict {
	if dir == "" {
		return verdict{Error: "-dir is required"}
	}
	if _, err := config.Load(dir, false); err != nil {
		return verdict{Error: "config.Load: " + err.Error()}
	}

	var (
		mu    sync.Mutex
		attrs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					for _, kv := range sp.Attributes {
						attrs = append(attrs, kv.Value.GetStringValue())
					}
				}
			}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	if !langfuse.Init("pk-test", "sk-test", srv.URL) {
		return verdict{Error: "langfuse client did not enable"}
	}
	c := langfuse.Get()

	stdout := fmt.Sprintf(
		"AWS_ACCESS_KEY_ID=%s\nSLACK_BOT_TOKEN=%s\nremote: %s\ncache hit for %s\nrecord %s\n",
		fakeAWSKey, fakeSlack, fakeCloneURL, falsePos, customSecret)

	ctx := c.TraceStart(context.Background(), langfuse.TraceParams{
		Name:     "e2e-turn",
		Input:    "clone " + fakeCloneURL,
		Metadata: map[string]any{"cmd": "git clone " + fakeCloneURL},
	})
	span := c.ToolStart(ctx, langfuse.ToolParams{
		Name:  "bash",
		Input: map[string]any{"command": "cat .env.prod"},
	})
	span.SetOutput(stdout)
	span.End()
	langfuse.SetTraceOutput(ctx, "done "+fakeAWSKey)
	c.TraceEnd(ctx)

	langfuse.ShutdownGlobal()

	mu.Lock()
	defer mu.Unlock()

	v := verdict{Attributes: len(attrs)}
	if len(attrs) == 0 {
		v.Error = "no span attributes reached the exporter"
		return v
	}

	joined := strings.Join(attrs, "\n")
	for _, secret := range []string{fakePAT, fakeAWSKey, fakeSlack} {
		if strings.Contains(joined, secret) {
			v.LeakedItems = append(v.LeakedItems, secret)
		}
	}
	for _, a := range attrs {
		for _, tok := range strings.Fields(a) {
			if i := strings.Index(tok, "[REDACTED:"); i >= 0 {
				if j := strings.Index(tok[i:], "]"); j > 0 {
					v.Markers = append(v.Markers, tok[i:i+j+1])
				}
			}
		}
	}
	v.FalsePos = strings.Contains(joined, falsePos)
	v.OK = len(v.LeakedItems) == 0
	return v
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(zr)
	}
	return io.ReadAll(r.Body)
}
