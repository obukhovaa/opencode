package langfuse

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/opencode-ai/opencode/internal/version"
)

const (
	envAttr       = "langfuse.environment"
	envNameAttr   = "deployment.environment.name"
	envLegacyAttr = "deployment.environment"
)

func TestNewResource(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		fallback   string            // LANGFUSE_TRACING_ENVIRONMENT
		env        map[string]string // extra variables for env:VAR_NAME
		want       string            // "" means no environment attribute
	}{
		{name: "set", configured: "staging", want: "staging"},
		{name: "unset", configured: ""},
		{name: "digits, hyphen and underscore", configured: "dev_2-eu", want: "dev_2-eu"},
		{name: "exactly 40 characters", configured: strings.Repeat("a", 40), want: strings.Repeat("a", 40)},
		{
			name:       "env:VAR_NAME resolves",
			configured: "env:AGENT_ENV",
			env:        map[string]string{"AGENT_ENV": "prod"},
			want:       "prod",
		},
		{
			// Same as the credentials: an env: reference that resolves to
			// nothing does not fall through to LANGFUSE_TRACING_ENVIRONMENT.
			name:       "env:VAR_NAME unset",
			configured: "env:AGENT_ENV",
			fallback:   "prod",
		},
		{name: "fallback when unset", fallback: "prod", want: "prod"},
		{name: "config wins over fallback", configured: "dev", fallback: "prod", want: "dev"},
		{name: "uppercase", configured: "Prod"},
		{name: "longer than 40 characters", configured: strings.Repeat("a", 41)},
		{name: "reserved langfuse prefix", configured: "langfuse-dev"},
		{name: "unexpanded placeholder", configured: "$AGENT_ENV"},
		{name: "braced placeholder", configured: "${AGENT_ENV}"},
		{name: "space", configured: "my env"},
		{name: "dot", configured: "dev.eu"},
		{name: "invalid fallback", fallback: "Prod"},
		{
			name:       "invalid value from env:VAR_NAME",
			configured: "env:AGENT_ENV",
			env:        map[string]string{"AGENT_ENV": "$AGENT_ENV"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(environmentEnvVar, tt.fallback)
			t.Setenv("AGENT_ENV", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			set := newResource(tt.configured).Set()

			if got, _ := set.Value(attribute.Key("service.name")); got.AsString() != tracerName {
				t.Errorf("service.name = %q, want %q", got.AsString(), tracerName)
			}
			if got, _ := set.Value(attribute.Key("service.version")); got.AsString() != version.Version {
				t.Errorf("service.version = %q, want %q", got.AsString(), version.Version)
			}
			if _, ok := set.Value(attribute.Key(envLegacyAttr)); ok {
				t.Errorf("%s is set; only %s and %s are written", envLegacyAttr, envAttr, envNameAttr)
			}

			for _, key := range []string{envAttr, envNameAttr} {
				got, ok := set.Value(attribute.Key(key))
				switch {
				case tt.want == "" && ok:
					t.Errorf("%s = %q, want it absent", key, got.AsString())
				case tt.want != "" && !ok:
					t.Errorf("%s absent, want %q", key, tt.want)
				case tt.want != "" && got.AsString() != tt.want:
					t.Errorf("%s = %q, want %q", key, got.AsString(), tt.want)
				}
			}
			wantLen := 2
			if tt.want != "" {
				wantLen = 4
			}
			if set.Len() != wantLen {
				t.Errorf("resource has %d attributes, want %d: %v", set.Len(), wantLen, set.ToSlice())
			}
		})
	}
}

// TestNewResource_WarnsOnInvalidEnvironment covers the "warning names the
// value and the reason" clause: dropping an invalid value must be logged,
// with the placeholder wording for a "$" value, and a valid value must not
// warn at all.
func TestNewResource_WarnsOnInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		wantLog []string // nil means no warning
	}{
		{name: "valid", env: "staging"},
		{name: "uppercase", env: "Prod", wantLog: []string{"dropping invalid trace environment", "environment=Prod", "'P'"}},
		{name: "placeholder", env: "$AGENT_ENV", wantLog: []string{"dropping invalid trace environment", "environment=$AGENT_ENV", "unexpanded placeholder"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(environmentEnvVar, "")
			var buf lockedBuffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			newResource(tt.env)

			logged := buf.String()
			if tt.wantLog == nil {
				if strings.Contains(logged, "level=WARN") {
					t.Errorf("valid environment %q logged a warning: %s", tt.env, logged)
				}
				return
			}
			if !strings.Contains(logged, "level=WARN") {
				t.Fatalf("no warning logged for %q; got: %q", tt.env, logged)
			}
			for _, want := range tt.wantLog {
				if !strings.Contains(logged, want) {
					t.Errorf("warning for %q lacks %q: %s", tt.env, want, logged)
				}
			}
		})
	}
}

// lockedBuffer is a strings.Builder safe to read while a handler writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestValidateEnvironment(t *testing.T) {
	tests := []struct {
		env    string
		errHas string // "" means valid
	}{
		{env: "prod"},
		{env: "dev_2-eu"},
		{env: strings.Repeat("a", 40)},
		{env: "my-langfuse"}, // only the prefix is reserved
		{env: "$AGENT_ENV", errHas: "unexpanded placeholder"},
		{env: "${AGENT_ENV}", errHas: "unexpanded placeholder"},
		{env: "Prod", errHas: "'P'"},
		{env: "dev.eu", errHas: "'.'"},
		{env: "my env", errHas: "' '"},
		{env: strings.Repeat("a", 41), errHas: "41 characters"},
		{env: "langfuse-dev", errHas: "reserves"},
		{env: "langfuse", errHas: "reserves"},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			err := validateEnvironment(tt.env)
			if tt.errHas == "" {
				if err != nil {
					t.Fatalf("validateEnvironment(%q) = %v, want nil", tt.env, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateEnvironment(%q) = nil, want an error", tt.env)
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("validateEnvironment(%q) = %v, want one containing %q", tt.env, err, tt.errHas)
			}
		})
	}
}

// TestNew_ExportsEnvironment goes through New and a real gzipped OTLP export,
// and reads the environment off the resource of the request that left the
// process. An invalid value must not disable the client: spans still arrive,
// just without an environment.
func TestNew_ExportsEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		env        map[string]string
		want       string
	}{
		{name: "literal", configured: "staging", want: "staging"},
		{name: "env:VAR_NAME", configured: "env:AGENT_ENV", env: map[string]string{"AGENT_ENV": "prod"}, want: "prod"},
		{name: "fallback", env: map[string]string{environmentEnvVar: "dev"}, want: "dev"},
		{name: "unset"},
		{name: "unexpanded placeholder keeps tracing on", configured: "$AGENT_ENV"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(environmentEnvVar, "")
			// The SDK merges these into the exported resource
			// (sdktrace.WithResource); blank them so the runner's
			// environment cannot add an environment key.
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
			t.Setenv("OTEL_SERVICE_NAME", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			srv, resources := captureOTLPResources(t)

			c := New("pk-test", "sk-test", srv.URL, tt.configured)
			if !c.Enabled() {
				t.Fatal("client disabled; the environment must never disable tracing")
			}
			ctx := c.TraceStart(context.Background(), TraceParams{Name: "turn"})
			c.TraceEnd(ctx)
			c.Shutdown()

			got := resources()
			if len(got) == 0 {
				t.Fatal("no spans reached the exporter")
			}
			for _, attrs := range got {
				if attrs["service.name"] != tracerName {
					t.Errorf("service.name = %q, want %q", attrs["service.name"], tracerName)
				}
				for _, key := range []string{envAttr, envNameAttr} {
					v, ok := attrs[key]
					if tt.want == "" && ok {
						t.Errorf("%s = %q, want it absent", key, v)
					}
					if tt.want != "" && v != tt.want {
						t.Errorf("%s = %q (present: %v), want %q", key, v, ok, tt.want)
					}
				}
			}
		})
	}
}

// captureOTLPResources starts an OTLP/HTTP endpoint and returns it with a
// function yielding the resource attributes of every ResourceSpans received.
func captureOTLPResources(t *testing.T) (*httptest.Server, func() []map[string]string) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer zr.Close()
			body = zr
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range req.ResourceSpans {
			attrs := map[string]string{}
			for _, kv := range rs.GetResource().GetAttributes() {
				attrs[kv.Key] = kv.Value.GetStringValue()
			}
			got = append(got, attrs)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]string(nil), got...)
	}
}
