package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

const (
	poolSonnet = string(models.BedrockSonnet55)
	poolKimi   = string(models.BedrockUSKimiK31)
	poolSol    = string(models.BedrockGPT61Sol)
	poolTerra  = string(models.BedrockGPT61Terra)
)

// poolJSON renders entries as the JSON a gate would emit.
func poolJSON(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func entry(model string, weight float64, extra ...any) map[string]any {
	e := map[string]any{"model": model, "weight": weight}
	for i := 0; i+1 < len(extra); i += 2 {
		e[extra[i].(string)] = extra[i+1]
	}
	return e
}

func poolStep(pool, seed, rate string) Step {
	return Step{ID: "review-code", ModelPool: pool, ModelSeed: seed, ModelShadowRate: rate}
}

// TestHashUnitGolden pins hashUnit to values computed independently (Python:
// int.from_bytes(sha256(s)[:8], "big") >> 11, + 0.5, / 2**53), so a pick can
// be reproduced outside the harness exactly as docs/flows.md describes.
func TestHashUnitGolden(t *testing.T) {
	golden := map[string]float64{
		"":     0.8894159948913374,
		"seed": 0.10037757989062374,
		"https://gitlab.com/g/p/-/merge_requests/1:shadow": 0.28056000255940733,
		"mr-42:bedrock.us-kimi-k3.1":                       0.8218185884509992,
		"mr-42:bedrock.eu-claude-sonnet-5-5":               0.22215287882370544,
	}
	for s, want := range golden {
		if got := hashUnit(s); got != want {
			t.Errorf("hashUnit(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestParseModelPool(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{`[]`, 0, false},
		{`[{"model":"a","weight":1},{"model":"b","weight":0,"effort":"high","shadowWeight":3,"note":"ignored"}]`, 2, false},
		{`{"model":"a","weight":1}`, 0, true},
		{`[{"model":"a","weight":"50"}]`, 0, true},
		{`[{"model":1,"weight":1}]`, 0, true},
		{`[{"model":"a","weight":1}] trailing`, 0, true},
		{`not json`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseModelPool(tt.raw)
			if (err != nil) != tt.wantErr || len(got) != tt.want {
				t.Fatalf("parseModelPool = %+v, %v; want %d entries, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestResolveStepModelPool(t *testing.T) {
	standard := poolJSON(t, entry(poolSonnet, 70), entry(poolKimi, 20), entry(poolSol, 10))
	tests := []struct {
		name    string
		step    Step
		args    map[string]any
		want    *poolPick
		wantErr bool
	}{
		{name: "no pool", step: Step{ID: "s"}},
		{name: "unresolved pool", step: poolStep("${args.review_pool}", "x", "")},
		{name: "empty pool", step: poolStep("[]", "x", "")},
		{name: "all weights zero", step: poolStep(poolJSON(t, entry(poolSonnet, 0), entry(poolKimi, 0)), "x", "")},
		{name: "only unknown models", step: poolStep(poolJSON(t, entry("bedrock.nope", 100)), "x", "")},
		{name: "malformed JSON fails", step: poolStep(`[{"model":`, "x", ""), wantErr: true},
		{name: "an object is not a pool", step: poolStep(`{"model":"x","weight":1}`, "x", ""), wantErr: true},
		// Golden picks, computed independently (see TestHashUnitGolden).
		{
			name: "golden pick mr-1",
			step: poolStep(standard, "mr-1", ""),
			want: &poolPick{Model: models.ModelID(poolSonnet), Bucket: 24},
		},
		{
			name: "golden pick mr-42 from args",
			step: poolStep("${args.review_pool}", "${args.mr}", "0"),
			args: map[string]any{"review_pool": []any{entry(poolSonnet, 70), entry(poolKimi, 20), entry(poolSol, 10)}, "mr": "mr-42"},
			want: &poolPick{Model: models.ModelID(poolKimi), Bucket: 66},
		},
		{
			name: "unknown and malformed entries are skipped",
			step: poolStep(poolJSON(t, entry("bedrock.nope", 100), entry("", 100), entry(poolKimi, -1), entry(poolSonnet, 1)), "mr-1", ""),
			want: &poolPick{Model: models.ModelID(poolSonnet), Bucket: 24},
		},
		{
			name: "entry effort wins over the step's",
			step: Step{ID: "review-code", ModelPool: poolJSON(t, entry(poolKimi, 1, "effort", "XHigh")), ModelSeed: "mr-1", ReasoningEffort: "low"},
			want: &poolPick{Model: models.ModelID(poolKimi), Effort: "xhigh", Bucket: 24},
		},
		{
			name: "step effort is the default and is clamped",
			step: Step{ID: "review-code", ModelPool: poolJSON(t, entry(poolTerra, 1)), ModelSeed: "mr-1", ReasoningEffort: "${args.effort}"},
			args: map[string]any{"effort": "xhigh"},
			want: &poolPick{Model: models.ModelID(poolTerra), Effort: "high", Bucket: 24},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveStepModelPool(tt.step, tt.args, map[string]any{"iteration": 1}, "root")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidModelPool) {
					t.Fatalf("error = %v, want ErrInvalidModelPool", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("pick = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveStepModelPool_SeedFallsBackToRootSession(t *testing.T) {
	pool := poolJSON(t, entry(poolSonnet, 50), entry(poolKimi, 50))
	fromRoot, _ := resolveStepModelPool(poolStep(pool, "", ""), nil, nil, "mr-42")
	explicit, _ := resolveStepModelPool(poolStep(pool, "mr-42", ""), nil, nil, "other-root")
	unresolved, _ := resolveStepModelPool(poolStep(pool, "${args.mr_links}", ""), map[string]any{}, nil, "mr-42")
	if fromRoot == nil || *fromRoot != *explicit || *unresolved != *explicit {
		t.Fatalf("root-seeded %+v, unresolved %+v, explicit %+v; want all equal", fromRoot, unresolved, explicit)
	}
}

// picksFor returns the primary pick for n seeds.
func picksFor(t *testing.T, pool string, n int) []models.ModelID {
	t.Helper()
	out := make([]models.ModelID, n)
	for i := range out {
		pick, err := resolveStepModelPool(poolStep(pool, fmt.Sprintf("seed-%d", i), ""), nil, nil, "root")
		if err != nil || pick == nil {
			t.Fatalf("seed %d: %+v, %v", i, pick, err)
		}
		out[i] = pick.Model
	}
	return out
}

func TestModelPoolDistribution(t *testing.T) {
	const n = 20000
	picks := picksFor(t, poolJSON(t, entry(poolSonnet, 70), entry(poolKimi, 20), entry(poolSol, 10)), n)
	counts := map[models.ModelID]int{}
	for _, p := range picks {
		counts[p]++
	}
	for model, want := range map[string]float64{poolSonnet: 0.70, poolKimi: 0.20, poolSol: 0.10} {
		if got := float64(counts[models.ModelID(model)]) / n; math.Abs(got-want) > 0.015 {
			t.Errorf("%s share = %.3f, want %.2f±0.015", model, got, want)
		}
	}
}

// TestModelPoolRendezvous pins the property weighted rendezvous hashing is
// chosen for: a change to the pool only moves seeds onto or off the entry
// that changed, never between the others.
func TestModelPoolRendezvous(t *testing.T) {
	const n = 5000
	base := picksFor(t, poolJSON(t, entry(poolSonnet, 60), entry(poolKimi, 40)), n)
	cases := []struct {
		name    string
		pool    string
		changed string
	}{
		{"add an entry", poolJSON(t, entry(poolSonnet, 60), entry(poolKimi, 40), entry(poolSol, 20)), poolSol},
		{"raise one weight", poolJSON(t, entry(poolSonnet, 60), entry(poolKimi, 80)), poolKimi},
		{"reorder the list", poolJSON(t, entry(poolKimi, 40), entry(poolSonnet, 60)), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			moved := 0
			for i, p := range picksFor(t, tc.pool, n) {
				if p == base[i] {
					continue
				}
				moved++
				if string(p) != tc.changed {
					t.Fatalf("seed %d moved %s -> %s, not onto the changed entry %q", i, base[i], p, tc.changed)
				}
			}
			if tc.changed != "" && moved == 0 {
				t.Fatal("no seed moved onto the changed entry")
			}
		})
	}
	t.Run("remove an entry", func(t *testing.T) {
		withSol := picksFor(t, poolJSON(t, entry(poolSonnet, 60), entry(poolKimi, 40), entry(poolSol, 20)), n)
		for i, p := range picksFor(t, poolJSON(t, entry(poolSonnet, 60), entry(poolKimi, 40)), n) {
			if withSol[i] != models.ModelID(poolSol) && p != withSol[i] {
				t.Fatalf("seed %d moved %s -> %s although its entry stayed", i, withSol[i], p)
			}
		}
	})
}

func TestModelPoolShadow(t *testing.T) {
	const n = 4000
	shadowRun := func(t *testing.T, pool, rate string) (sampled int, shadows map[models.ModelID]int) {
		t.Helper()
		shadows = map[models.ModelID]int{}
		for i := 0; i < n; i++ {
			pick, err := resolveStepModelPool(poolStep(pool, fmt.Sprintf("seed-%d", i), rate), nil, nil, "root")
			if err != nil || pick == nil {
				t.Fatalf("seed %d: %+v, %v", i, pick, err)
			}
			if pick.Shadow != (pick.ShadowModel != "") {
				t.Fatalf("shadow flag and model disagree: %+v", pick)
			}
			if pick.Shadow {
				sampled++
				shadows[pick.ShadowModel]++
				if pick.ShadowModel == pick.Model {
					t.Fatalf("shadow model equals the primary: %+v", pick)
				}
			}
			if pick.Bucket < 0 || pick.Bucket > 99 {
				t.Fatalf("bucket %d out of range", pick.Bucket)
			}
			if rate == "25" && pick.Shadow != (pick.Bucket < 25) {
				t.Fatalf("with an integer rate, shadow must equal bucket < rate: %+v", pick)
			}
		}
		return sampled, shadows
	}

	pool := poolJSON(t, entry(poolSonnet, 70), entry(poolKimi, 30))
	if sampled, _ := shadowRun(t, pool, "0"); sampled != 0 {
		t.Errorf("rate 0 sampled %d", sampled)
	}
	if sampled, _ := shadowRun(t, pool, "100"); sampled != n {
		t.Errorf("rate 100 sampled %d of %d", sampled, n)
	}
	if sampled, _ := shadowRun(t, pool, "25"); math.Abs(float64(sampled)/n-0.25) > 0.03 {
		t.Errorf("rate 25 sampled %.3f", float64(sampled)/n)
	}
	for _, bad := range []string{"-1", "101", "lots", "NaN"} {
		pick, err := resolveStepModelPool(poolStep(pool, "seed-1", bad), nil, nil, "root")
		if err != nil || pick == nil || pick.Shadow {
			t.Errorf("invalid rate %q: %+v, %v; want a pick without a shadow", bad, pick, err)
		}
	}
	if sampled, _ := shadowRun(t, poolJSON(t, entry(poolSonnet, 100)), "100"); sampled != 0 {
		t.Errorf("a single-entry pool sampled %d shadows", sampled)
	}

	// A weight-0 newcomer gets every shadow of a one-model pool and no
	// primary traffic; shadowWeight 0 keeps an entry out of shadows.
	newcomer := poolJSON(t, entry(poolSonnet, 100), entry(poolSol, 0, "effort", "max"))
	_, shadows := shadowRun(t, newcomer, "100")
	if shadows[models.ModelID(poolSol)] != n {
		t.Errorf("shadow draws = %v, want every one on the weight-0 entry", shadows)
	}
	pick, _ := resolveStepModelPool(poolStep(newcomer, "seed-1", "100"), nil, nil, "root")
	if pick.Model != models.ModelID(poolSonnet) || pick.ShadowEffort != "max" {
		t.Errorf("newcomer pick = %+v", pick)
	}
	if sampled, _ := shadowRun(t, poolJSON(t, entry(poolSonnet, 100), entry(poolSol, 0, "shadowWeight", 0)), "100"); sampled != 0 {
		t.Errorf("shadowWeight 0 still sampled %d shadows", sampled)
	}
	// The shadow draw follows shadowWeight, not weight: behind a Sonnet
	// primary, Kimi (shadowWeight 90) should take about 90% of the shadows
	// and Sol (10) the rest, although both have weight 50.
	weighted := poolJSON(t, entry(poolSonnet, 100), entry(poolKimi, 50, "shadowWeight", 90), entry(poolSol, 50, "shadowWeight", 10))
	behindSonnet, onKimi := 0, 0
	for i := 0; i < n; i++ {
		pick, _ := resolveStepModelPool(poolStep(weighted, fmt.Sprintf("seed-%d", i), "100"), nil, nil, "root")
		if pick.Model != models.ModelID(poolSonnet) {
			continue
		}
		behindSonnet++
		if pick.ShadowModel == models.ModelID(poolKimi) {
			onKimi++
		}
	}
	if share := float64(onKimi) / float64(behindSonnet); behindSonnet == 0 || math.Abs(share-0.9) > 0.04 {
		t.Errorf("Kimi share of the shadows behind Sonnet = %.3f (%d seeds), want about 0.9", share, behindSonnet)
	}
}

func TestRecordPoolPick(t *testing.T) {
	args := map[string]any{"pool": map[string]any{"other": map[string]any{"model": "x"}}}
	prev := args["pool"].(map[string]any)
	recordPoolPick(args, "review-code", &poolPick{Model: "m", Effort: "high", Bucket: 7, Shadow: true, ShadowModel: "s", ShadowEffort: "low"})
	got := args["pool"].(map[string]any)
	want := map[string]any{"model": "m", "effort": "high", "bucket": 7, "shadow": true, "shadow_model": "s", "shadow_effort": "low"}
	if fmt.Sprint(got["review-code"]) != fmt.Sprint(want) || got["other"] == nil {
		t.Fatalf("pool = %v", got)
	}
	if _, mutated := prev["review-code"]; mutated {
		t.Fatal("the previous pool map was mutated")
	}
	recordPoolPick(args, "review-code", nil)
	if _, ok := args["pool"].(map[string]any)["review-code"]; ok {
		t.Fatal("an inactive pool left its stale pick")
	}
	recordPoolPick(args, "other", nil)
	if _, ok := args["pool"]; ok {
		t.Fatal("an empty pool map was left behind")
	}
}

func TestMergeOutputArgsSkipsPool(t *testing.T) {
	args := map[string]any{"pool": map[string]any{"review-code": map[string]any{"shadow": false}}}
	mergeOutputArgs(args, map[string]any{"verdict": "ok", "pool": map[string]any{"review-code": map[string]any{"shadow": true}}})
	if args["verdict"] != "ok" {
		t.Fatal("ordinary fields were not merged")
	}
	if shadow := args["pool"].(map[string]any)["review-code"].(map[string]any)["shadow"]; shadow != false {
		t.Fatal("a structured output overwrote the pool pick")
	}
}

func TestValidateFlow_ModelPool(t *testing.T) {
	step := func(mut func(*Step)) Flow {
		s := Step{ID: "review-code", Prompt: "x"}
		mut(&s)
		return Flow{ID: "pool", Spec: FlowSpec{Steps: []Step{s}}}
	}
	tests := []struct {
		name    string
		flow    Flow
		wantErr error
	}{
		{"templated keys", step(func(s *Step) {
			s.ModelPool, s.ModelSeed, s.ModelShadowRate = "${args.review_pool}", "${args.mr_links}", "${args.review_shadow_rate}"
		}), nil},
		{"literal pool with an unknown model only warns", step(func(s *Step) {
			s.ModelPool, s.ModelShadowRate = `[{"model":"bedrock.newcomer","weight":0},{"model":"`+poolSonnet+`","weight":100}]`, "12.5"
		}), nil},
		{"malformed literal pool", step(func(s *Step) { s.ModelPool = `[{"model":` }), ErrInvalidModelPool},
		{"negative literal weight", step(func(s *Step) { s.ModelPool = `[{"model":"` + poolSonnet + `","weight":-5}]` }), ErrInvalidModelPool},
		{"bad literal effort", step(func(s *Step) { s.ModelPool = `[{"model":"` + poolSonnet + `","weight":5,"effort":"turbo"}]` }), ErrInvalidModelPool},
		{"literal rate out of range", step(func(s *Step) { s.ModelPool, s.ModelShadowRate = "${args.p}", "150" }), ErrInvalidModelPool},
		{"seed without a pool", step(func(s *Step) { s.ModelSeed = "${args.mr_links}" }), ErrInvalidModelPool},
		{"rate without a pool", step(func(s *Step) { s.ModelShadowRate = "10" }), ErrInvalidModelPool},
		{"output schema declares pool", step(func(s *Step) {
			s.Output = &StepOutput{Schema: map[string]any{"type": "object", "properties": map[string]any{"pool": map[string]any{"type": "object"}}}}
		}), ErrReservedArg},
		{"args schema declares pool", Flow{ID: "pool", Spec: FlowSpec{
			Args:  map[string]any{"type": "object", "properties": map[string]any{"pool": map[string]any{"type": "string"}}},
			Steps: []Step{{ID: "a", Prompt: "x"}},
		}}, ErrReservedArg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlow(&tt.flow)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateArgsRejectsPool(t *testing.T) {
	err := validateArgs(map[string]any{"pool": map[string]any{"review-code": map[string]any{"shadow": true}}}, nil)
	if !errors.Is(err, ErrReservedArg) || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("error = %v, want ErrReservedArg", err)
	}
}

// TestRunStep_ModelPool drives a review-lane-shaped flow: the pool picks the
// review step's model before NewAgent and before the prompt is rendered, and
// the shadow branch routes only on `${args.pool.review-code.shadow} == true`
// — which is false for an unsampled seed and for a step without a pool.
func TestRunStep_ModelPool(t *testing.T) {
	newFlow := func(id string) Flow {
		return Flow{
			ID:   id,
			Name: "Test Model Pool",
			Spec: FlowSpec{Steps: []Step{
				{
					ID:              "review-code",
					Prompt:          "Review on ${args.pool.review-code.model} at ${args.pool.review-code.effort}.",
					Model:           poolSonnet,
					ReasoningEffort: "xhigh",
					ModelPool:       "${args.review_pool}",
					ModelSeed:       "${args.mr_links}",
					ModelShadowRate: "${args.review_shadow_rate}",
					Rules: []Rule{
						{Then: "approve"},
						{If: "${args.pool.review-code.shadow} == true", Then: "review-code-shadow"},
					},
				},
				{ID: "approve", Prompt: "approve"},
				{
					ID:              "review-code-shadow",
					Prompt:          "Shadow review.",
					Model:           "${args.pool.review-code.shadow_model}",
					ReasoningEffort: "${args.pool.review-code.shadow_effort}",
					BestEffort:      true,
				},
			}},
		}
	}
	pool := []any{entry(poolSonnet, 70), entry(poolKimi, 30)}
	mrLinks := []any{"https://gitlab.com/g/p/-/merge_requests/1"}
	want, err := resolveStepModelPool(poolStep(poolJSON(t, entry(poolSonnet, 70), entry(poolKimi, 30)), `["https://gitlab.com/g/p/-/merge_requests/1"]`, "100"),
		nil, nil, "unused")
	if err != nil || want == nil {
		t.Fatalf("expected pick: %+v, %v", want, err)
	}

	tests := []struct {
		name       string
		args       map[string]any
		wantModel  models.ModelID
		wantEffort string
		wantShadow bool
	}{
		{"sampled", map[string]any{"review_pool": pool, "mr_links": mrLinks, "review_shadow_rate": 100}, want.Model, "xhigh", true},
		{"not sampled", map[string]any{"review_pool": pool, "mr_links": mrLinks, "review_shadow_rate": 0}, want.Model, "xhigh", false},
		{"no pool", map[string]any{"mr_links": mrLinks}, models.ModelID(poolSonnet), "xhigh", false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFlow(fmt.Sprintf("test-model-pool-%d", i))
			registerTestFlow(t, f)
			factory := newModelAgentFactory(models.BedrockSonnet55)
			review := newStubAgent()
			factory.agents["review-code"] = review
			svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, factory)
			agentEvents, flowStates, err := svc.Run(t.Context(), "prefix", f.ID, tt.args, true)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			states := drainFlow(t, agentEvents, flowStates)

			calls := lastCallByStep(factory.snapshotNewAgentCalls())
			if got := calls["review-code"].override; got.Model != tt.wantModel || got.ReasoningEffort != tt.wantEffort {
				t.Errorf("review-code override = %+v, want %s at %s", got, tt.wantModel, tt.wantEffort)
			}
			if st := findLatestByStepID(states, "review-code"); st == nil || st.Model != string(tt.wantModel) {
				t.Errorf("review-code state = %+v, want model %s", st, tt.wantModel)
			}
			prompts := review.snapshotPrompts()
			if tt.name != "no pool" && (len(prompts) != 1 || !strings.Contains(prompts[0], "Review on "+string(want.Model))) {
				t.Errorf("review prompt = %q, want the picked model in it", prompts)
			}
			_, shadowRan := calls["review-code-shadow"]
			if shadowRan != tt.wantShadow {
				t.Fatalf("shadow step ran = %v, want %v", shadowRan, tt.wantShadow)
			}
			if shadowRan {
				got := calls["review-code-shadow"].override
				if got.Model != want.ShadowModel || got.Model == want.Model {
					t.Errorf("shadow override = %+v, want the shadow model %s", got, want.ShadowModel)
				}
			}
			if _, approved := calls["approve"]; !approved {
				t.Error("the approve step did not run")
			}
		})
	}
}
