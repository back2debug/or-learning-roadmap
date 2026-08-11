package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Summarize appends a numbered findings summary for this run to the console
// and learning_log.txt, in the same shape as the hand-written summaries that
// preceded it. Everything is computed from the results recorded in memory, so
// the summary always reflects exactly the run logged above it.
//
// Every finding must survive into learning_log.txt: each section returns its
// lines here rather than printing, so the block is written in one place
// through Note (console + file) and can never end up console-only.
func (l *Loggers) Summarize() {
	if len(l.results) == 0 {
		return
	}

	sections := [][]string{
		l.routingFindings(),
		l.failureFindings(),
		l.truncationFindings(),
		l.costFindings(),
		l.latencyFindings(),
		l.stickyFindings(),
	}

	l.Note("")
	l.Note("=== Findings summary (run of %s, %d requests, max_tokens=%d) ===",
		time.Now().Format("2006-01-02 15:04"), len(l.results), maxTokens)
	l.Note("")

	n := 0
	for _, sec := range sections {
		if len(sec) == 0 {
			continue
		}
		n++
		l.Note("%d. %s", n, sec[0])
		for _, line := range sec[1:] {
			l.Note("   %s", line)
		}
		l.Note("")
	}
	for _, line := range l.takeaway() {
		l.Note("%s", line)
	}
}

// routingFindings reports which concrete model each min_coding_score resolved
// to during the score sweep, and whether simple and complex prompts agreed.
func (l *Loggers) routingFindings() []string {
	routes := map[float64]map[string]bool{} // score -> set of models chosen
	var scores []float64
	for _, r := range l.results {
		if r.Phase != "score-sweep" || r.Err != "" || r.MinCodingScore == nil {
			continue
		}
		s := *r.MinCodingScore
		if routes[s] == nil {
			routes[s] = map[string]bool{}
			scores = append(scores, s)
		}
		routes[s][r.ModelSelected] = true
	}
	if len(scores) == 0 {
		return nil
	}
	sort.Float64s(scores)

	lines := []string{"Tier routing (min_coding_score -> model the router selected):"}
	for _, s := range scores {
		models := make([]string, 0, len(routes[s]))
		for m := range routes[s] {
			models = append(models, m)
		}
		sort.Strings(models)
		if len(models) == 1 {
			lines = append(lines, fmt.Sprintf("%.1f -> %s for every prompt type.", s, models[0]))
		} else {
			lines = append(lines, fmt.Sprintf("%.1f split across prompt types: %s.", s, strings.Join(models, ", ")))
		}
	}
	return lines
}

// failureFindings counts errored requests and lists each distinct error once.
func (l *Loggers) failureFindings() []string {
	counts := map[string]int{}
	var order []string
	failed := 0
	for _, r := range l.results {
		if r.Err == "" {
			continue
		}
		failed++
		if counts[r.Err] == 0 {
			order = append(order, r.Err)
		}
		counts[r.Err]++
	}
	if failed == 0 {
		return []string{fmt.Sprintf("Failures: none — all %d requests succeeded.", len(l.results))}
	}
	lines := []string{fmt.Sprintf("Failures: %d of %d requests errored:", failed, len(l.results))}
	for _, e := range order {
		lines = append(lines, fmt.Sprintf("%dx %s", counts[e], e))
	}
	return lines
}

// truncationFindings reports responses cut off by the max_tokens cap. A
// truncated response is not an error — the request succeeded and was billed in
// full — so it is invisible in the failure count, which is exactly why it
// needs its own finding.
func (l *Loggers) truncationFindings() []string {
	perType := map[string]map[string]int{} // prompt type -> model -> count
	var typeOrder []string
	cut := 0
	var worst *Result // truncated response with the least visible text
	for i := range l.results {
		r := &l.results[i]
		if r.Err != "" || r.FinishReason != "length" {
			continue
		}
		cut++
		if perType[r.PromptType] == nil {
			perType[r.PromptType] = map[string]int{}
			typeOrder = append(typeOrder, r.PromptType)
		}
		perType[r.PromptType][r.ModelSelected]++
		if worst == nil || len(r.ResponseText) < len(worst.ResponseText) {
			worst = r
		}
	}
	if cut == 0 {
		return []string{fmt.Sprintf("Truncation: none — no response hit the max_tokens=%d cap.", maxTokens)}
	}

	lines := []string{fmt.Sprintf(
		"Truncation: %d of %d responses stopped at the max_tokens=%d cap (finish_reason=length), "+
			"billed in full despite being incomplete:", cut, len(l.results), maxTokens)}
	sort.Strings(typeOrder)
	for _, pt := range typeOrder {
		models := make([]string, 0, len(perType[pt]))
		for m := range perType[pt] {
			models = append(models, m)
		}
		sort.Strings(models)
		parts := make([]string, 0, len(models))
		for _, m := range models {
			if c := perType[pt][m]; c > 1 {
				parts = append(parts, fmt.Sprintf("%s (x%d)", m, c))
			} else {
				parts = append(parts, m)
			}
		}
		lines = append(lines, fmt.Sprintf("%s prompt on %s.", pt, strings.Join(parts, ", ")))
	}
	if worst != nil {
		lines = append(lines, fmt.Sprintf(
			"Worst payoff: %s emitted %d chars of visible text (%d chars of streamed reasoning) for its %d output tokens.",
			worst.ModelSelected, len(worst.ResponseText), worst.ReasoningChars, worst.OutputTokens))
	}
	return lines
}

// span tracks the range a value took across several requests, so a model that
// was measured more than once (e.g. sol at both 0.8 and 1.0) reports honestly
// as a range instead of whichever sample happened to land last.
type span struct {
	min, max float64
	set      bool
}

func (s *span) add(v float64) {
	if !s.set {
		s.min, s.max, s.set = v, v, true
		return
	}
	if v < s.min {
		s.min = v
	}
	if v > s.max {
		s.max = v
	}
}

func (s span) format(unit string) string {
	if s.max-s.min < 1e-9 {
		return fmt.Sprintf(unit, s.min)
	}
	return fmt.Sprintf(unit+"-"+unit, s.min, s.max)
}

// byModel collects a per-model span over successful sweep results matching
// promptType, returning models ordered by ascending lower bound.
func (l *Loggers) byModel(promptType string, value func(*Result) (float64, bool)) ([]string, map[string]*span) {
	spans := map[string]*span{}
	for i := range l.results {
		r := &l.results[i]
		if r.Phase != "score-sweep" || r.Err != "" || r.PromptType != promptType {
			continue
		}
		v, ok := value(r)
		if !ok {
			continue
		}
		if spans[r.ModelSelected] == nil {
			spans[r.ModelSelected] = &span{}
		}
		spans[r.ModelSelected].add(v)
	}
	models := make([]string, 0, len(spans))
	for m := range spans {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return spans[models[i]].min < spans[models[j]].min })
	return models, spans
}

func cost(r *Result) (float64, bool) { return r.Cost, r.CostKnown }

func seconds(r *Result) (float64, bool) { return float64(r.TotalMs) / 1000.0, true }

// costFindings reports what each tier charged per prompt type and the overall
// spread, since cost separation across tiers is the point of the lab.
func (l *Loggers) costFindings() []string {
	var lines []string
	for _, pt := range []string{"simple", "complex"} {
		models, spans := l.byModel(pt, cost)
		if len(models) == 0 {
			continue
		}
		parts := make([]string, 0, len(models))
		for _, m := range models {
			parts = append(parts, fmt.Sprintf("%s %s", m, spans[m].format("$%.5f")))
		}
		lines = append(lines, fmt.Sprintf("%s prompt: %s.", pt, strings.Join(parts, ", ")))
	}
	if len(lines) == 0 {
		return nil
	}

	var cheapest, priciest *Result
	for i := range l.results {
		r := &l.results[i]
		if r.Err != "" || r.Phase != "score-sweep" || !r.CostKnown {
			continue
		}
		if cheapest == nil || r.Cost < cheapest.Cost {
			cheapest = r
		}
		if priciest == nil || r.Cost > priciest.Cost {
			priciest = r
		}
	}
	head := "Cost by tier:"
	if cheapest != nil && priciest != nil && cheapest != priciest && cheapest.Cost > 0 {
		head = fmt.Sprintf("Cost by tier — the sweep spanned %.0fx, $%.6f (%s, %s) to $%.6f (%s, %s):",
			priciest.Cost/cheapest.Cost,
			cheapest.Cost, cheapest.ModelSelected, cheapest.PromptType,
			priciest.Cost, priciest.ModelSelected, priciest.PromptType)
	}
	return append([]string{head}, lines...)
}

// latencyFindings reports per-tier wall time per prompt type plus the single
// slowest request of the run.
func (l *Loggers) latencyFindings() []string {
	var lines []string
	for _, pt := range []string{"simple", "complex"} {
		models, spans := l.byModel(pt, seconds)
		if len(models) == 0 {
			continue
		}
		parts := make([]string, 0, len(models))
		for _, m := range models {
			parts = append(parts, fmt.Sprintf("%s %s", m, spans[m].format("%.1fs")))
		}
		lines = append(lines, fmt.Sprintf("%s prompt: %s.", pt, strings.Join(parts, ", ")))
	}
	if len(lines) == 0 {
		return nil
	}

	var slowest *Result
	for i := range l.results {
		r := &l.results[i]
		if r.Err != "" {
			continue
		}
		if slowest == nil || r.TotalMs > slowest.TotalMs {
			slowest = r
		}
	}
	head := "Latency by tier:"
	if slowest != nil {
		head = fmt.Sprintf("Latency by tier — slowest request was %s on the %s prompt at %.1fs (TTFT %.1fs):",
			slowest.ModelSelected, slowest.PromptType,
			float64(slowest.TotalMs)/1000.0, float64(slowest.TTFTMs)/1000.0)
	}
	return append([]string{head}, lines...)
}

// stickyFindings reports, across iterations, how many follow-ups stayed on
// the model their session opened with, and what the control request did.
func (l *Loggers) stickyFindings() []string {
	firstBySession := map[string]string{} // session_id -> model
	stayed, moved := 0, 0
	controls := map[string]bool{}
	for _, r := range l.results {
		if r.Err != "" {
			continue
		}
		switch r.Phase {
		case "sticky-first":
			firstBySession[r.SessionID] = r.ModelSelected
		case "sticky-follow-up":
			if first, ok := firstBySession[r.SessionID]; ok {
				if r.ModelSelected == first {
					stayed++
				} else {
					moved++
				}
			}
		case "sticky-control":
			controls[r.ModelSelected] = true
		}
	}
	if stayed == 0 && moved == 0 {
		// Sticky phase failed or never ran; the per-iteration narration
		// already explains why, so stay quiet here.
		return nil
	}

	var lines []string
	if moved == 0 {
		lines = append(lines, fmt.Sprintf(
			"Sticky sessions: confirmed — %s stayed on their session's first model.", plural(stayed, "follow-up")))
	} else {
		lines = append(lines, fmt.Sprintf(
			"Sticky sessions: %d follow-ups stayed, %d moved off their session's first model.", stayed, moved))
	}
	for _, m := range sortedKeys(controls) {
		if firstMatches(firstBySession, m) {
			lines = append(lines, fmt.Sprintf(
				"The no-session control also chose %s — expected while it is the tier's cheapest pick, so not evidence against stickiness.", m))
		} else {
			lines = append(lines, fmt.Sprintf(
				"The no-session control chose %s instead — fresh routing diverged from the pinned session.", m))
		}
	}
	return lines
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstMatches(firstBySession map[string]string, model string) bool {
	for _, m := range firstBySession {
		if m == model {
			return true
		}
	}
	return false
}

// takeaway closes the block with the run's actionable conclusions. It states
// only what this run's numbers support: the cheapest-to-priciest tier gap, and
// whether the token budget — not the router — was the binding constraint.
func (l *Loggers) takeaway() []string {
	models, costs := l.byModel("complex", cost)
	var lines []string

	if len(models) >= 2 {
		lo, hi := models[0], models[len(models)-1]
		if costs[lo].min > 0 {
			lines = append(lines, fmt.Sprintf(
				"Takeaway: on the complex prompt the low tier (%s) ran %.0fx cheaper than the high tier (%s), %s vs %s.",
				lo, costs[hi].max/costs[lo].min, hi,
				costs[lo].format("$%.5f"), costs[hi].format("$%.5f")))
		}
	}

	cut, cutCost := 0, 0.0
	for i := range l.results {
		r := &l.results[i]
		if r.Err == "" && r.FinishReason == "length" {
			cut++
			if r.CostKnown && r.Cost > cutCost {
				cutCost = r.Cost
			}
		}
	}
	if cut > 0 {
		lines = append(lines, fmt.Sprintf(
			"The binding constraint this run was max_tokens=%d, not the router: %d responses were cut off mid-answer",
			maxTokens, cut))
		lines = append(lines, fmt.Sprintf(
			"and still billed in full (up to $%.5f each). Raise the cap or narrow the prompt before reading", cutCost))
		lines = append(lines, "anything into the quality of the truncated answers.")
	} else {
		lines = append(lines, fmt.Sprintf(
			"Every response finished inside the max_tokens=%d budget, so the tier differences above reflect", maxTokens))
		lines = append(lines, "routing and model behaviour rather than a truncated answer.")
	}
	return lines
}
