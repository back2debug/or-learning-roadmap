package main

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/assert"
	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

const (
	stable = string(config.RouterAuto)
	beta   = string(config.RouterAutoBeta)
)

func join(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	return strings.Join(ss, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func report(w io.Writer, runIDs []string, trials []assert.Trial, gens map[string]generation, skipped int) {
	cells := assert.Cells(trials)
	clean := 0
	spend := 0.0
	for _, t := range trials {
		spend += t.CostUSD
		if assert.Clean(t) {
			clean++
		}
	}
	fmt.Fprintf(w, "## Generated report\n\nRuns: %s. %d live trials, %d clean (succeeded, classified, answered by the model the router chose). Reported spend $%.4f.",
		md(strings.Join(runIDs, ", ")), len(trials), clean, spend)
	if skipped > 0 {
		fmt.Fprintf(w, " %d log lines were not trial records and were skipped.", skipped)
	}
	fmt.Fprint(w, "\n\nModel names are the router's decision (`resolved_to`), not the model that answered, which differs when a fallback model serves the request. Cross-cell comparisons use clean isolate trials only.\n")

	divergence(w, cells)
	classification(w, trials)
	defaults(w, cells)
	findings(w, trials)
	priceCap(w, cells)
	transports(w, cells)
	stickiness(w, trials, cells)
	frequency(w, trials)
	costLatency(w, trials, gens)
	perTrial(w, trials)
}

// divergence is the headline: the same prompt and the same settings on both
// tracks. Since auto-beta is the early-access track, a difference is a
// candidate for behaviour that has not reached the stable track yet.
func divergence(w io.Writer, cells []assert.Cell) {
	type pair struct{ s, b *assert.Cell }
	pairs := map[string]*pair{}
	var order []string
	for _, c := range cells {
		exp, _, _ := strings.Cut(c.ID, "/")
		key := c.Transport + "\x00" + exp
		p := pairs[key]
		if p == nil {
			p = &pair{}
			pairs[key] = p
			order = append(order, key)
		}
		switch c.Router {
		case stable:
			p.s = &c
		case beta:
			p.b = &c
		}
	}
	fmt.Fprint(w, "\n### Track divergence: `auto` vs `auto-beta` on identical prompts and settings\n\n")
	total, differ := 0, 0
	var rows [][]string
	for _, key := range order {
		p := pairs[key]
		if p.s == nil || p.b == nil {
			continue
		}
		total++
		same := assert.SameDecisions(*p.s, *p.b)
		if !same {
			differ++
		}
		exp, _, _ := strings.Cut(p.s.ID, "/")
		rows = append(rows, []string{exp, p.s.Transport, p.s.PromptID, p.s.Setting(), join(p.s.Decisions()), join(p.b.Decisions()), yesNo(same)})
	}
	if total == 0 {
		fmt.Fprint(w, "No head-to-head pairs with clean trials on both tracks.\n")
		return
	}
	fmt.Fprintf(w, "The tracks chose different models in **%d of %d** head-to-head pairs. Each difference is a place where beta behaves differently from stable today; whether it is new behaviour on its way to stable or a beta-only experiment cannot be told from one snapshot.\n\n", differ, total)
	header(w, "Experiment", "Transport", "Prompt", "Setting", "`auto` chose", "`auto-beta` chose", "Same")
	for _, r := range rows {
		row(w, r...)
	}
}

// classification asks whether task_type is stable across repeats and whether
// the two tracks classify the same prompt the same way. It uses every
// successful trial, since classification does not depend on session state.
func classification(w io.Writer, trials []assert.Trial) {
	type key struct{ prompt, router string }
	types := map[key][]string{}
	expected := map[string]string{}
	var prompts []string
	for _, t := range trials {
		if t.Status < 200 || t.Status >= 300 {
			continue
		}
		if !slices.Contains(prompts, t.PromptID) {
			prompts = append(prompts, t.PromptID)
		}
		expected[t.PromptID] = t.ExpectedTask
		tt := "(none)"
		if t.TaskType != nil && *t.TaskType != "" {
			tt = *t.TaskType
		}
		k := key{t.PromptID, t.Router}
		if !slices.Contains(types[k], tt) {
			types[k] = append(types[k], tt)
			slices.Sort(types[k])
		}
	}
	fmt.Fprint(w, "\n### Task classification\n\n")
	header(w, "Prompt", "Expected", "`auto` classified as", "`auto-beta` classified as", "Stable across repeats", "Tracks agree")
	agree, both := 0, 0
	for _, p := range prompts {
		s, b := types[key{p, stable}], types[key{p, beta}]
		same := len(s) > 0 && slices.Equal(s, b)
		if len(s) > 0 && len(b) > 0 {
			both++
			if same {
				agree++
			}
		}
		row(w, p, expected[p], join(s), join(b), yesNo(len(s) <= 1 && len(b) <= 1), yesNo(same))
	}
	fmt.Fprintf(w, "\nThe tracks agreed on %d of %d prompts. \"Expected\" is the harness author's guess, recorded for comparison, not an assertion.\n", agree, both)
}

// defaults answers "what does sending no cost setting behave like": for each
// group it lists the configured cells whose choices equal the baseline's.
func defaults(w io.Writer, cells []assert.Cell) {
	fmt.Fprint(w, "\n### Effective default: which settings match sending none\n\n")
	header(w, "Baseline cell", "Prompt", "No setting chose", "Settings that chose the same", "Settings that chose differently")
	n := 0
	for _, b := range cells {
		if b.Plugin != nil {
			continue
		}
		var same, diff []string
		for _, c := range cells {
			if c.Plugin == nil || c.Group() != b.Group() {
				continue
			}
			if base := assert.Baseline(c, cells); base == nil || base.ID != b.ID {
				continue
			}
			// Only pure cost settings say anything about the default.
			if len(c.Plugin.AllowedModels) > 0 || len(c.Plugin.ExcludedModels) > 0 || (c.Plugin.CostTier != nil && c.Plugin.CostQualityTradeoff != nil) {
				continue
			}
			if assert.SameDecisions(c, b) {
				same = append(same, assert.Describe(c.Plugin, nil))
			} else {
				diff = append(diff, assert.Describe(c.Plugin, nil))
			}
		}
		if len(same)+len(diff) == 0 {
			continue
		}
		n++
		row(w, b.ID, b.PromptID, join(b.Decisions()), join(same), join(diff))
	}
	if n == 0 {
		fmt.Fprint(w, "\nNo baseline has cost-setting cells to compare with in these runs.\n")
	}
}

func findings(w io.Writer, trials []assert.Trial) {
	titles := map[string]string{
		"observable_effect":    "Config with no observable effect",
		"cost_tier_direction":  "Cost tier sweep: does the band move?",
		"cost_tier_precedence": "Precedence: `cost_tier` vs `cost_quality_tradeoff`",
	}
	notes := map[string]string{
		"observable_effect":    "A flag means the cell chose exactly what the unconfigured baseline chose, so this cell alone cannot tell \"honoured\" from \"ignored\". It is not evidence the setting was ignored: a setting equal to the default looks the same.",
		"cost_tier_direction":  "Pass means the tier chose a different, dearer model than the tier below it: the band excluded the cheaper choice as well as bounding the dear end.",
		"cost_tier_precedence": "Each cell sent both settings pulling in opposite directions, and is compared with the cells that sent only one.",
	}
	all := assert.CheckRun(trials)
	for _, check := range []string{"cost_tier_precedence", "cost_tier_direction", "observable_effect"} {
		var fs []assert.Finding
		for _, f := range all {
			if f.Check == check && (check != "observable_effect" || f.Status == assert.Flag) {
				fs = append(fs, f)
			}
		}
		fmt.Fprintf(w, "\n### %s\n\n%s\n\n", titles[check], notes[check])
		if len(fs) == 0 {
			fmt.Fprint(w, "Nothing to report in these runs.\n")
			continue
		}
		header(w, "Cell", "Result", "Detail")
		for _, f := range fs {
			row(w, f.Cells[0], string(f.Status), f.Detail)
		}
	}
}

// priceCap compares unconfigured cells that differ only in their provider
// block: the control for whether a max_price cap changes the decision.
func priceCap(w io.Writer, cells []assert.Cell) {
	fmt.Fprint(w, "\n### Control: does a `max_price` cap change the routing decision?\n\n")
	n, changed := 0, 0
	var rows [][]string
	for _, a := range cells {
		if a.Plugin != nil || a.Provider != nil {
			continue
		}
		for _, b := range cells {
			if b.Plugin != nil || b.Provider == nil || b.Scope() != a.Scope() {
				continue
			}
			n++
			same := assert.SameDecisions(a, b)
			if !same {
				changed++
			}
			rows = append(rows, []string{a.Router, a.PromptID, a.ID, join(a.Decisions()), b.ID, join(b.Decisions()), yesNo(same)})
		}
	}
	if n == 0 {
		fmt.Fprint(w, "No with/without pairs in these runs (see `experiments-followup.yaml`).\n")
		return
	}
	fmt.Fprintf(w, "The cap changed the decision in %d of %d pairs. Every unconfigured cell with a cap is paired with the uncapped cell for the same router and prompt, so a prompt appears once per capped cell.\n\n", changed, n)
	header(w, "Router", "Prompt", "Cell with no provider block", "Chose", "Cell with a cap", "Chose", "Same")
	for _, r := range rows {
		row(w, r...)
	}
}

// transports pairs cells that differ only in API surface.
func transports(w io.Writer, cells []assert.Cell) {
	fmt.Fprint(w, "\n### Transport divergence: Chat Completions vs Messages\n\n")
	key := func(c assert.Cell) string { return c.Router + "\x00" + c.PromptID + "\x00" + c.Setting() }
	chat := map[string]assert.Cell{}
	for _, c := range cells {
		if c.Transport == string(config.TransportChatCompletions) {
			if _, dup := chat[key(c)]; !dup {
				chat[key(c)] = c
			}
		}
	}
	n, differ := 0, 0
	var rows [][]string
	for _, m := range cells {
		if m.Transport != string(config.TransportMessages) {
			continue
		}
		c, ok := chat[key(m)]
		if !ok {
			continue
		}
		n++
		same := assert.SameDecisions(c, m)
		if !same {
			differ++
		}
		rows = append(rows, []string{m.Router, m.PromptID, m.Setting(), join(c.Decisions()), join(m.Decisions()), yesNo(same)})
	}
	if n == 0 {
		fmt.Fprint(w, "No cells in these runs have a counterpart on the other transport.\n")
		return
	}
	fmt.Fprintf(w, "The two transports chose different models in %d of %d mirrored pairs.\n\n", differ, n)
	header(w, "Router", "Prompt", "Setting", "Chat Completions chose", "Messages chose", "Same")
	for _, r := range rows {
		row(w, r...)
	}
}

// stickiness lays out each session turn by turn and, for every turn that
// follows a change of cost setting, says whether the session's previous model
// was carried over or the router chose what it would choose fresh.
//
// Routing is deterministic for a given prompt and config, so a turn that
// repeats the previous turn's config proves nothing either way. Only a turn
// whose fresh choice differs from the previous turn's model can separate
// "remembered" from "chosen again", and the fresh choice comes from the
// isolate cell with the same router, transport, prompt and config.
func stickiness(w io.Writer, trials []assert.Trial, cells []assert.Cell) {
	fmt.Fprint(w, "\n### Stickiness: does a session carry its model?\n\n")
	fresh := func(t assert.Trial) (string, bool) {
		for _, c := range cells {
			if c.Router == t.Router && c.Transport == t.Transport && c.PromptID == t.PromptID &&
				assert.Describe(c.Plugin, c.Provider) == assert.Describe(t.Plugin, t.ProviderConfig) {
				if d := c.Decisions(); len(d) == 1 {
					return d[0], true
				}
			}
		}
		return "", false
	}
	sessions := map[string][]assert.Trial{}
	var order []string
	for _, t := range trials {
		if t.Stickiness != string(config.StickinessSticky) || t.SessionID == nil || !assert.Clean(t) {
			continue
		}
		if _, ok := sessions[*t.SessionID]; !ok {
			order = append(order, *t.SessionID)
		}
		sessions[*t.SessionID] = append(sessions[*t.SessionID], t)
	}
	tally := map[string]map[string]int{}
	var rows [][]string
	for _, sid := range order {
		turns := sessions[sid]
		for i, t := range turns {
			verdict := "opening turn"
			if i > 0 {
				prev := turns[i-1]
				f, ok := fresh(t)
				switch {
				case prev.Router != t.Router:
					verdict = "router changed"
					if ok && t.Decision() == prev.Decision() && f != prev.Decision() {
						verdict = "CARRIED across routers"
					}
				case !ok:
					verdict = "no fresh reference"
				case f == prev.Decision():
					verdict = "cannot tell (fresh choice equals previous model)"
				case t.Decision() == prev.Decision():
					verdict = "CARRIED (kept previous model; fresh would be " + f + ")"
				case t.Decision() == f:
					verdict = "FRESH (previous model dropped)"
				default:
					verdict = "neither previous nor fresh (fresh would be " + f + ")"
				}
				for _, k := range []string{"CARRIED", "FRESH"} {
					if strings.HasPrefix(verdict, k) {
						if tally[t.Router] == nil {
							tally[t.Router] = map[string]int{}
						}
						scope := "same task"
						if prev.PromptID != t.PromptID {
							scope = "task changed"
						}
						tally[t.Router][scope+" "+k]++
					}
				}
			}
			// The run id prefix is noise in a table.
			label := sid
			if parts := strings.SplitN(sid, "-", 3); len(parts) == 3 {
				label = parts[2]
			}
			rows = append(rows, []string{label, fmt.Sprint(i + 1), t.Router, t.PromptID, assert.Describe(t.Plugin, nil), t.Decision(), t.Provider, verdict})
		}
	}
	if len(rows) == 0 {
		fmt.Fprint(w, "No sticky sessions in these runs.\n")
		return
	}
	fmt.Fprint(w, "Decisive turns only: those where the fresh choice differs from the previous turn's model, so keeping it and choosing again look different. A session that carries its model contributes one carried turn per later turn.\n\n")
	header(w, "Router", "Same task: carried", "Same task: routed fresh", "Task changed: carried", "Task changed: routed fresh")
	for _, r := range []string{stable, beta} {
		row(w, r, fmt.Sprint(tally[r]["same task CARRIED"]), fmt.Sprint(tally[r]["same task FRESH"]),
			fmt.Sprint(tally[r]["task changed CARRIED"]), fmt.Sprint(tally[r]["task changed FRESH"]))
	}
	fmt.Fprint(w, "\n")
	header(w, "Session", "Turn", "Router", "Prompt", "Setting", "Chose", "Provider", "Verdict")
	for _, r := range rows {
		row(w, r...)
	}
}

// frequency is model selection per router, task type and cost setting.
func frequency(w io.Writer, trials []assert.Trial) {
	type key struct{ router, task, setting, model string }
	counts := map[key]int{}
	for _, t := range trials {
		if t.Stickiness != string(config.StickinessIsolate) || !assert.Clean(t) {
			continue
		}
		counts[key{t.Router, *t.TaskType, assert.Describe(t.Plugin, nil), t.Decision()}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return strings.Join([]string{a.task, a.setting, a.router, a.model}, "\x00") < strings.Join([]string{b.task, b.setting, b.router, b.model}, "\x00")
	})
	fmt.Fprint(w, "\n### Model selection frequency by router, task type and cost setting\n\n")
	header(w, "Task type", "Setting", "Router", "Model chosen", "Trials")
	for _, k := range keys {
		row(w, k.task, k.setting, k.router, k.model, fmt.Sprint(counts[k]))
	}
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// costLatency reports cost and latency per cell. Where the backfill has a
// generation's stats, its total_cost is the ground truth; otherwise the cost
// is what the response reported inline.
func costLatency(w io.Writer, trials []assert.Trial, gens map[string]generation) {
	type agg struct {
		n, backfilled              int
		inline, minC, maxC, billed float64
		lat                        []int64
		tokens                     int
	}
	cells := map[string]*agg{}
	var order []string
	for _, t := range trials {
		if t.Status < 200 || t.Status >= 300 {
			continue
		}
		a := cells[t.CellID]
		if a == nil {
			a = &agg{minC: t.CostUSD, maxC: t.CostUSD}
			cells[t.CellID] = a
			order = append(order, t.CellID)
		}
		a.n++
		a.inline += t.CostUSD
		a.minC, a.maxC = min(a.minC, t.CostUSD), max(a.maxC, t.CostUSD)
		a.lat = append(a.lat, t.LatencyMS)
		if g, ok := gens[t.GenerationID]; ok && g.TotalCost != nil {
			a.backfilled++
			a.billed += *g.TotalCost
			if g.NativeCompletion != nil {
				a.tokens += *g.NativeCompletion
			}
		}
	}
	fmt.Fprint(w, "\n### Cost and latency per cell\n\nSuccessful trials. \"Billed\" is the backfilled `total_cost` from `/api/v1/generation`, shown where the backfill has it; latency includes any upstream retries OpenRouter made.\n\n")
	header(w, "Cell", "Trials", "Mean cost (inline)", "Min", "Max", "Mean billed (backfill)", "Backfilled", "Mean native completion tokens", "Median latency ms", "Max latency ms")
	for _, id := range order {
		a := cells[id]
		billed, toks := "-", "-"
		if a.backfilled > 0 {
			billed = fmt.Sprintf("$%.6f", a.billed/float64(a.backfilled))
			toks = fmt.Sprint(a.tokens / a.backfilled)
		}
		row(w, id, fmt.Sprint(a.n), fmt.Sprintf("$%.6f", a.inline/float64(a.n)), fmt.Sprintf("$%.6f", a.minC), fmt.Sprintf("$%.6f", a.maxC),
			billed, fmt.Sprintf("%d/%d", a.backfilled, a.n), toks, fmt.Sprint(median(a.lat)), fmt.Sprint(slices.Max(a.lat)))
	}
}

// perTrial is the assertion tally and every trial that failed or was flagged.
func perTrial(w io.Writer, trials []assert.Trial) {
	type problem struct {
		t assert.Trial
		r assert.Result
	}
	var names []string
	tally := map[string]map[assert.Status]int{}
	var problems []problem
	for _, t := range trials {
		for _, r := range assert.CheckTrial(t) {
			if tally[r.Check] == nil {
				tally[r.Check] = map[assert.Status]int{}
				names = append(names, r.Check)
			}
			tally[r.Check][r.Status]++
			if r.Status == assert.Fail || r.Status == assert.Flag {
				problems = append(problems, problem{t, r})
			}
		}
	}
	fmt.Fprint(w, "\n### Per-trial assertions\n\n")
	header(w, "Check", "Pass", "Fail", "Flag", "Skip")
	for _, n := range names {
		c := tally[n]
		row(w, n, fmt.Sprint(c[assert.Pass]), fmt.Sprint(c[assert.Fail]), fmt.Sprint(c[assert.Flag]), fmt.Sprint(c[assert.Skip]))
	}
	fmt.Fprint(w, "\n### Trials that failed an assertion or were set aside\n\nA failed or flagged trial is excluded from every comparison above.\n\n")
	if len(problems) == 0 {
		fmt.Fprint(w, "None.\n")
		return
	}
	header(w, "Run", "Cell", "Rep", "Check", "Status", "Detail")
	for _, p := range problems {
		row(w, p.t.RunID, p.t.CellID, fmt.Sprintf("%d.%d", p.t.Repeat, p.t.Step), p.r.Check, string(p.r.Status), p.r.Detail)
	}
}
