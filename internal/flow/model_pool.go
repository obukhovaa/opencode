package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Model pools: a step's modelPool / modelSeed / modelShadowRate pick the
// step's model and effort deterministically per seed and may sample a shadow
// model. docs/flows.md ("Model pools and shadow runs") is the user-facing
// contract, including the exact hashing, so a pick can be reproduced outside
// the harness; the design rationale is in the genai-375-model-pools OpenSpec
// change.

// ReservedPoolArg is the arg the runtime writes pool picks under, as
// args.pool.<step id>. Callers, args schemas and step outputs may not use it.
const ReservedPoolArg = "pool"

// modelPoolEntry is one element of a modelPool JSON array. Unknown keys are
// ignored, so a newer pool shape still loads on an older harness.
type modelPoolEntry struct {
	Model        string   `json:"model"`
	Weight       float64  `json:"weight"`
	Effort       string   `json:"effort,omitempty"`
	ShadowWeight *float64 `json:"shadowWeight,omitempty"`
}

// shadowWeight is the entry's weight in the shadow draw: shadowWeight when
// set, else weight, with weight 0 counting as 1 so an entry kept out of
// primary traffic still gets shadow samples.
func (e modelPoolEntry) shadowWeight() float64 {
	switch {
	case e.ShadowWeight != nil:
		return *e.ShadowWeight
	case e.Weight > 0:
		return e.Weight
	default:
		return 1
	}
}

// poolPick is a resolved pool: the primary model and effort the step runs
// on, the seed's shadow-sampling bucket, and the shadow model and effort
// when the seed was sampled. It is written to args.pool.<step id>.
type poolPick struct {
	Model        models.ModelID
	Effort       string
	Bucket       int
	Shadow       bool
	ShadowModel  models.ModelID
	ShadowEffort string
}

// argsValue is the pick as args.pool.<step id> holds it. The key names are a
// contract with flows that route on them (`${args.pool.<step>.shadow}`).
func (p poolPick) argsValue() map[string]any {
	return map[string]any{
		"model":         string(p.Model),
		"effort":        p.Effort,
		"bucket":        p.Bucket,
		"shadow":        p.Shadow,
		"shadow_model":  string(p.ShadowModel),
		"shadow_effort": p.ShadowEffort,
	}
}

// hashUnit maps s to the open interval (0, 1): the top 53 bits of the first
// 8 bytes of sha256(s), read big-endian, centred in their bucket.
func hashUnit(s string) float64 {
	sum := sha256.Sum256([]byte(s))
	x := binary.BigEndian.Uint64(sum[:8])
	return (float64(x>>11) + 0.5) / (1 << 53)
}

// rendezvousPick returns the index of the entry with the highest weighted
// rendezvous score weight / -ln(hashUnit(prefix + model)), skipping weights
// that are not positive; ties go to the smaller model id. -1 when no entry
// has a positive weight.
func rendezvousPick(entries []modelPoolEntry, weight func(modelPoolEntry) float64, prefix string) int {
	best, bestScore := -1, 0.0
	for i, e := range entries {
		w := weight(e)
		if w <= 0 {
			continue
		}
		score := w / -math.Log(hashUnit(prefix+e.Model))
		if best < 0 || score > bestScore || (score == bestScore && e.Model < entries[best].Model) {
			best, bestScore = i, score
		}
	}
	return best
}

// parseModelPool decodes a resolved modelPool value. Only JSON that is not
// an array of entry objects is an error; entry-level problems are left to
// usablePoolEntries (run time) and validateStepModelPool (load time).
func parseModelPool(raw string) ([]modelPoolEntry, error) {
	var entries []modelPoolEntry
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	if err := dec.Decode(&entries); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the array")
	}
	return entries, nil
}

// poolEntryProblem describes what makes an entry unusable apart from an
// unknown model, or "" when it is well formed.
func poolEntryProblem(e modelPoolEntry) string {
	switch {
	case strings.TrimSpace(e.Model) == "":
		return "empty model"
	case e.Weight < 0 || math.IsNaN(e.Weight) || math.IsInf(e.Weight, 0):
		return "weight must be a non-negative number"
	case e.ShadowWeight != nil && (*e.ShadowWeight < 0 || math.IsNaN(*e.ShadowWeight) || math.IsInf(*e.ShadowWeight, 0)):
		return "shadowWeight must be a non-negative number"
	case e.Effort != "" && !models.IsReasoningEffort(e.Effort):
		return "effort must be one of low|medium|high|xhigh|max"
	}
	return ""
}

// usablePoolEntries keeps the entries the step can run on, warning about
// each one it drops: malformed, naming a model this build does not know
// (a pool can be configured ahead of the deployed image), or repeating an
// earlier entry's model.
func usablePoolEntries(stepID string, entries []modelPoolEntry) []modelPoolEntry {
	usable := make([]modelPoolEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		e.Model = strings.TrimSpace(e.Model)
		reason := poolEntryProblem(e)
		if reason == "" {
			if _, ok := models.SupportedModels[models.ModelID(e.Model)]; !ok {
				reason = "model unknown to this build"
			} else if seen[e.Model] {
				reason = "duplicate model"
			}
		}
		if reason != "" {
			logging.Warn("Skipping a model pool entry", "step", stepID, "model", e.Model, "reason", reason)
			continue
		}
		seen[e.Model] = true
		usable = append(usable, e)
	}
	return usable
}

// resolveStepModelPool resolves the step's modelPool against args. It
// returns nil, nil when the step declares no pool or the pool is inactive
// (empty, unresolved, `[]`, no usable entry, all weights 0) — the step's own
// `model` / `reasoningEffort` then apply — and an ErrInvalidModelPool error
// when the value is not a JSON array of entries. rootSessionID is the seed
// when modelSeed resolves to nothing.
func resolveStepModelPool(step Step, args map[string]any, stepVars map[string]any, rootSessionID string) (*poolPick, error) {
	if strings.TrimSpace(step.ModelPool) == "" {
		return nil, nil
	}
	raw := strings.TrimSpace(substituteScoped(step.ModelPool, args, stepVars))
	if raw == "" || strings.Contains(raw, "${") {
		logging.Warn("Step modelPool unresolved, using the step's model", "step", step.ID, "modelPool", step.ModelPool)
		return nil, nil
	}
	entries, err := parseModelPool(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: step %q: %v", ErrInvalidModelPool, step.ID, err)
	}
	usable := usablePoolEntries(step.ID, entries)
	seed := poolSeed(step, args, stepVars, rootSessionID)
	primary := rendezvousPick(usable, func(e modelPoolEntry) float64 { return e.Weight }, seed+":")
	if primary < 0 {
		if len(entries) > 0 {
			logging.Warn("Step modelPool has no usable entry with a positive weight, using the step's model", "step", step.ID)
		}
		return nil, nil
	}

	stepEffort := resolvedStepEffort(step, args, stepVars)
	pick := &poolPick{
		Model:  models.ModelID(usable[primary].Model),
		Effort: poolEffort(step.ID, usable[primary], stepEffort),
	}
	u := hashUnit(seed + ":shadow")
	pick.Bucket = int(u * 100)
	if u*100 < poolShadowRate(step, args, stepVars) {
		others := append(append([]modelPoolEntry(nil), usable[:primary]...), usable[primary+1:]...)
		if i := rendezvousPick(others, modelPoolEntry.shadowWeight, seed+":shadow:"); i >= 0 {
			pick.Shadow = true
			pick.ShadowModel = models.ModelID(others[i].Model)
			pick.ShadowEffort = poolEffort(step.ID, others[i], stepEffort)
		}
	}
	return pick, nil
}

// poolSeed resolves modelSeed, falling back to the root session id.
func poolSeed(step Step, args map[string]any, stepVars map[string]any, rootSessionID string) string {
	seed := strings.TrimSpace(substituteScoped(step.ModelSeed, args, stepVars))
	if seed == "" || strings.Contains(seed, "${") {
		logging.Warn("Step modelSeed is empty or unresolved, seeding the model pool with the root session id",
			"step", step.ID, "modelSeed", step.ModelSeed)
		return rootSessionID
	}
	return seed
}

// poolShadowRate resolves modelShadowRate to a percentage in [0, 100];
// unset is 0, anything else unusable is 0 with a warning.
func poolShadowRate(step Step, args map[string]any, stepVars map[string]any) float64 {
	if strings.TrimSpace(step.ModelShadowRate) == "" {
		return 0
	}
	raw := strings.TrimSpace(substituteScoped(step.ModelShadowRate, args, stepVars))
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(rate) || rate < 0 || rate > 100 {
		logging.Warn("Step modelShadowRate is not a number from 0 to 100, sampling no shadow",
			"step", step.ID, "modelShadowRate", step.ModelShadowRate, "resolved", raw)
		return 0
	}
	return rate
}

// resolvedStepEffort is the step's own reasoningEffort, substituted and
// lower-cased; "" when unset or unresolved.
func resolvedStepEffort(step Step, args map[string]any, stepVars map[string]any) string {
	effort := strings.ToLower(strings.TrimSpace(substituteScoped(step.ReasoningEffort, args, stepVars)))
	if strings.Contains(effort, "${") {
		return ""
	}
	return effort
}

// poolEffort is the entry's effort, else the step's, clamped to what the
// entry's model accepts.
func poolEffort(stepID string, e modelPoolEntry, stepEffort string) string {
	want := strings.ToLower(e.Effort)
	if want == "" {
		want = stepEffort
	}
	got := models.ClampReasoningEffort(models.SupportedModels[models.ModelID(e.Model)], want)
	if got != want {
		logging.Warn("Model pool effort clamped to what the model accepts",
			"step", stepID, "model", e.Model, "effort", want, "clamped", got)
	}
	return got
}

// recordPoolPick writes pick to args.pool.<stepID>, or removes a stale entry
// for the step when pick is nil. The pool map is replaced, not mutated, so
// a map an earlier state still references is left alone.
func recordPoolPick(args map[string]any, stepID string, pick *poolPick) {
	prev, _ := args[ReservedPoolArg].(map[string]any)
	if pick == nil {
		if _, ok := prev[stepID]; !ok {
			return
		}
	}
	next := make(map[string]any, len(prev)+1)
	for k, v := range prev {
		next[k] = v
	}
	if pick == nil {
		delete(next, stepID)
	} else {
		next[stepID] = pick.argsValue()
	}
	if len(next) == 0 {
		delete(args, ReservedPoolArg)
		return
	}
	args[ReservedPoolArg] = next
}

// mergeOutputArgs merges a step's structured output into args, except the
// reserved pool key: a model must not be able to forge a pool pick.
func mergeOutputArgs(args, output map[string]any) {
	for k, v := range output {
		if k == ReservedPoolArg {
			logging.Warn("Ignoring a structured-output field named after the reserved pool arg")
			continue
		}
		args[k] = v
	}
}

// validateStepModelPool checks a step's pool keys at load: modelSeed and
// modelShadowRate need modelPool, and literal values must be well formed.
// A literal pool naming a model this build does not know only warns, as at
// run time.
func validateStepModelPool(step Step) error {
	if strings.TrimSpace(step.ModelPool) == "" {
		if strings.TrimSpace(step.ModelSeed) != "" || strings.TrimSpace(step.ModelShadowRate) != "" {
			return fmt.Errorf("%w: step %q sets modelSeed or modelShadowRate without modelPool", ErrInvalidModelPool, step.ID)
		}
		return nil
	}
	if !strings.Contains(step.ModelPool, "${") {
		entries, err := parseModelPool(step.ModelPool)
		if err != nil {
			return fmt.Errorf("%w: step %q: %v", ErrInvalidModelPool, step.ID, err)
		}
		for i, e := range entries {
			if reason := poolEntryProblem(e); reason != "" {
				return fmt.Errorf("%w: step %q entry %d: %s", ErrInvalidModelPool, step.ID, i, reason)
			}
			if _, ok := models.SupportedModels[models.ModelID(strings.TrimSpace(e.Model))]; !ok {
				logging.Warn("Model pool entry names a model this build does not know; it will be skipped",
					"step", step.ID, "model", e.Model)
			}
		}
	}
	if rate := strings.TrimSpace(step.ModelShadowRate); rate != "" && !strings.Contains(rate, "${") {
		v, err := strconv.ParseFloat(rate, 64)
		if err != nil || math.IsNaN(v) || v < 0 || v > 100 {
			return fmt.Errorf("%w: step %q modelShadowRate %q must be a number from 0 to 100", ErrInvalidModelPool, step.ID, rate)
		}
	}
	return nil
}

// schemaDeclaresPool reports whether a JSON Schema declares a top-level
// `pool` property.
func schemaDeclaresPool(schema map[string]any) bool {
	props, _ := schema["properties"].(map[string]any)
	_, ok := props[ReservedPoolArg]
	return ok
}
