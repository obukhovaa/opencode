package clitool

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
)

// Environment variables that tune the limits every CLI tool inherits for
// the fields its manifest leaves unset. Each overrides the matching
// `cliTools` key of .opencode.json, which overrides the built-in default.
// They are read wherever manifests are loaded, so the native tools and
// `opencode tools serve` apply the same values.
const (
	TimeoutEnv        = "OPENCODE_CLI_TOOLS_TIMEOUT"
	MaxTimeoutEnv     = "OPENCODE_CLI_TOOLS_MAX_TIMEOUT"
	MaxOutputBytesEnv = "OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES"
)

// Sources name where a resolved default came from (`opencode tools list`).
const (
	SourceBuiltin = "builtin"
	SourceConfig  = "config"
	SourceEnv     = "env"
)

// Defaults are the limits a manifest inherits for the fields it does not
// set: the per-call timeout, the cap on the per-call `timeout` parameter,
// and the output cap in bytes (negative = unbounded).
type Defaults struct {
	Timeout        time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int

	// *Source record which layer supplied each value.
	TimeoutSource        string
	MaxTimeoutSource     string
	MaxOutputBytesSource string
}

// BuiltinDefaults are the compiled-in limits.
func BuiltinDefaults() Defaults {
	return Defaults{
		Timeout:              DefaultTimeout,
		MaxTimeout:           DefaultMaxTimeout,
		MaxOutputBytes:       DefaultMaxOutputBytes,
		TimeoutSource:        SourceBuiltin,
		MaxTimeoutSource:     SourceBuiltin,
		MaxOutputBytesSource: SourceBuiltin,
	}
}

// ResolveDefaults layers the `cliTools` configuration block and then the
// environment over the built-in limits. Per knob the precedence is
// env > config > builtin. A value that does not parse or is out of range
// is reported in the returned warnings and skipped, so the next layer
// applies. A resolved maxTimeout below the resolved timeout is raised to
// it, with a warning. The result is what every manifest loaded with it
// inherits (see ParseWithDefaults); a manifest's own fields still win.
func ResolveDefaults(cfg *config.CLIToolsConfig) (Defaults, []string) {
	d := BuiltinDefaults()
	var warns []string
	warn := func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }

	if cfg != nil {
		applyDuration(&d.Timeout, &d.TimeoutSource, cfg.Timeout, "cliTools.timeout", SourceConfig, warn)
		applyDuration(&d.MaxTimeout, &d.MaxTimeoutSource, cfg.MaxTimeout, "cliTools.maxTimeout", SourceConfig, warn)
		if cfg.MaxOutputBytes != 0 {
			d.MaxOutputBytes, d.MaxOutputBytesSource = normalizeOutputCap(cfg.MaxOutputBytes), SourceConfig
		}
	}
	applyDuration(&d.Timeout, &d.TimeoutSource, os.Getenv(TimeoutEnv), TimeoutEnv, SourceEnv, warn)
	applyDuration(&d.MaxTimeout, &d.MaxTimeoutSource, os.Getenv(MaxTimeoutEnv), MaxTimeoutEnv, SourceEnv, warn)
	if raw := strings.TrimSpace(os.Getenv(MaxOutputBytesEnv)); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			warn("%s: %q is not an integer number of bytes; ignored", MaxOutputBytesEnv, raw)
		case n != 0:
			d.MaxOutputBytes, d.MaxOutputBytesSource = normalizeOutputCap(n), SourceEnv
		}
	}
	if d.MaxTimeout < d.Timeout {
		warn("maxTimeout %s (%s) is below timeout %s (%s); raised to %s",
			d.MaxTimeout, d.MaxTimeoutSource, d.Timeout, d.TimeoutSource, d.Timeout)
		d.MaxTimeout, d.MaxTimeoutSource = d.Timeout, d.TimeoutSource
	}
	return d, warns
}

// applyDuration parses raw (a Go duration or a number of seconds) into dst
// when it is set and positive; otherwise it records a warning naming where
// the value came from and leaves dst alone.
func applyDuration(dst *time.Duration, src *string, raw, where, source string, warn func(string, ...any)) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	v, err := parseDuration(raw, 0)
	switch {
	case err != nil:
		warn("%s: %v; ignored", where, err)
	case v <= 0:
		warn("%s: %q must be positive; ignored", where, raw)
	default:
		*dst, *src = v, source
	}
}

// normalizeOutputCap maps every "no cap" spelling to -1.
func normalizeOutputCap(n int) int {
	if n < 0 {
		return -1
	}
	return n
}

// normalized fills zero fields from the built-in limits and keeps
// maxTimeout at or above timeout, so a hand-built Defaults (tests, callers
// of ParseWithDefaults) is always usable.
func (d Defaults) normalized() Defaults {
	b := BuiltinDefaults()
	if d.Timeout <= 0 {
		d.Timeout, d.TimeoutSource = b.Timeout, b.TimeoutSource
	}
	if d.MaxTimeout <= 0 {
		d.MaxTimeout, d.MaxTimeoutSource = b.MaxTimeout, b.MaxTimeoutSource
	}
	if d.MaxTimeout < d.Timeout {
		d.MaxTimeout, d.MaxTimeoutSource = d.Timeout, d.TimeoutSource
	}
	if d.MaxOutputBytes == 0 {
		d.MaxOutputBytes, d.MaxOutputBytesSource = b.MaxOutputBytes, b.MaxOutputBytesSource
	} else {
		d.MaxOutputBytes = normalizeOutputCap(d.MaxOutputBytes)
	}
	for _, s := range []*string{&d.TimeoutSource, &d.MaxTimeoutSource, &d.MaxOutputBytesSource} {
		if *s == "" {
			*s = SourceBuiltin
		}
	}
	return d
}
