// Package analyze turns a run's outcomes into the cross-dialect reports:
// capability matrix, upstream drift, parameter fates, error taxonomy,
// streaming grammar, and the final ANALYSIS.md.
package analyze

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/runner"
)

var dialects = []string{"chat", "responses", "messages"}

// WriteReports generates every report file into dir.
func WriteReports(dir string, data *runner.RunData, transcriptPath string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("analyze: creating reports dir: %w", err)
	}
	files := map[string]string{
		"CAPABILITY-MATRIX.md": capabilityMatrix(data),
		"STREAMING-GRAMMAR.md": streamingGrammar(data),
		"ANALYSIS.md":          analysis(data, transcriptPath),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return fmt.Errorf("analyze: writing %s: %w", name, err)
		}
	}
	return nil
}

// ---- capability matrix ----

func capabilityMatrix(data *runner.RunData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Capability matrix\n\nRun %s · models: %s\n\n", data.RunID, strings.Join(data.Models, ", "))
	b.WriteString("| entry | kind |")
	for _, d := range dialects {
		fmt.Fprintf(&b, " %s |", d)
	}
	b.WriteString("\n|---|---|---|---|---|\n")

	for _, entry := range entryIDs(data) {
		kind := ""
		cells := map[string]string{}
		for _, d := range dialects {
			var oks, total int
			errs := map[string]int{}
			for _, o := range data.Outcomes {
				if o.EntryID != entry || o.Dialect != d {
					continue
				}
				kind = o.Kind
				total++
				switch {
				case o.APIError != nil:
					errs[fmt.Sprintf("%d %s", o.Status, orDash(o.APIError.ErrorType))]++
				case o.TransportErr != "":
					errs["transport"]++
				case o.Result != nil:
					oks++
				}
			}
			switch {
			case total == 0:
				cells[d] = "–"
			case oks == total:
				cells[d] = fmt.Sprintf("✅ %d/%d", oks, total)
			case oks == 0 && len(errs) > 0:
				cells[d] = "❌ " + joinCounts(errs)
			default:
				cells[d] = fmt.Sprintf("⚠️ %d/%d ok; %s", oks, total, joinCounts(errs))
			}
		}
		fmt.Fprintf(&b, "| %s | %s |", entry, kind)
		for _, d := range dialects {
			fmt.Fprintf(&b, " %s |", cells[d])
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ---- streaming grammar ----

func streamingGrammar(data *runner.RunData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Streaming grammar\n\nDistinct SSE event shapes observed per dialect (run %s).\n", data.RunID)
	for _, d := range dialects {
		type sample struct {
			payload string
			count   int
		}
		samples := map[string]*sample{}
		var order []string
		for _, o := range data.Outcomes {
			if o.Dialect != d || !o.Streamed {
				continue
			}
			for _, ev := range o.Events {
				typ := ev.Type
				if typ == "" {
					typ = "(untyped data chunk)"
				}
				if s, ok := samples[typ]; ok {
					s.count++
					continue
				}
				samples[typ] = &sample{payload: truncateStr(string(ev.Data), 200), count: 1}
				order = append(order, typ)
			}
		}
		fmt.Fprintf(&b, "\n## %s\n\n", d)
		if len(order) == 0 {
			b.WriteString("No streamed events observed.\n")
			continue
		}
		for _, typ := range order {
			s := samples[typ]
			fmt.Fprintf(&b, "- **%s** (%d seen)\n\n  ```json\n  %s\n  ```\n", typ, s.count, s.payload)
		}
	}
	return b.String()
}

// ---- the main analysis document ----

func analysis(data *runner.RunData, transcriptPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# orlab analysis — run %s\n\n", data.RunID)
	fmt.Fprintf(&b, "- started: %s, finished: %s (%s)\n", data.StartedAt.Format(time.RFC3339),
		data.FinishedAt.Format(time.RFC3339), data.FinishedAt.Sub(data.StartedAt).Round(time.Second))
	fmt.Fprintf(&b, "- models: %s\n", strings.Join(data.Models, ", "))
	fmt.Fprintf(&b, "- outcomes: %d", len(data.Outcomes))
	if data.DryRun {
		b.WriteString(" (dry run: nothing was sent)")
	}
	if data.Partial {
		b.WriteString("\n\n> ⚠️ **Partial run** — interrupted before completion; every table below undercounts.")
	}
	b.WriteString("\n\n")

	b.WriteString("## What we learned\n\n")
	for _, f := range findings(data) {
		fmt.Fprintf(&b, "- %s\n", f)
	}

	b.WriteString("\n## Capability matrix\n\n")
	b.WriteString("See [CAPABILITY-MATRIX.md](CAPABILITY-MATRIX.md); regenerated every run.\n")

	b.WriteString("\n## Upstream body drift (echo)\n\n")
	b.WriteString(driftSection(data))

	b.WriteString("\n## Parameter fates\n\n")
	b.WriteString(fateSection(data))

	b.WriteString("\n## Error taxonomy\n\n")
	b.WriteString(taxonomySection(data))

	b.WriteString("\n## Cost & latency ledger\n\n")
	b.WriteString(ledgerSection(data))

	b.WriteString("\n## Streamed vs buffered\n\n")
	b.WriteString(bufferedSection(data))

	if v := varianceSection(data); v != "" {
		b.WriteString("\n## Determinism probe\n\n")
		b.WriteString(v)
	}

	fmt.Fprintf(&b, "\n## Appendix\n\n- Phase transcript: `%s`\n- Streaming grammar: [STREAMING-GRAMMAR.md](STREAMING-GRAMMAR.md)\n", transcriptPath)
	return b.String()
}

// ---- findings ----

func findings(data *runner.RunData) []string {
	var out []string
	if data.DryRun {
		return []string{"Dry run: request bodies were built and diffed, nothing was sent."}
	}

	// /messages with non-Anthropic models.
	var msgOK, msgTotal int
	for _, o := range data.Outcomes {
		if o.Dialect != "messages" || strings.HasPrefix(o.Model, "anthropic/") || o.Kind == "error_probe" {
			continue
		}
		msgTotal++
		if o.Result != nil {
			msgOK++
		}
	}
	if msgTotal > 0 {
		out = append(out, fmt.Sprintf("**/messages with non-Anthropic models**: %d/%d requests succeeded — %s",
			msgOK, msgTotal, verdict(msgOK, msgTotal)))
	}

	// Echo support per dialect.
	for _, d := range dialects {
		var withEcho, streamed int
		for _, o := range data.Outcomes {
			if o.Dialect != d || !o.Streamed || o.Result == nil {
				continue
			}
			streamed++
			if len(o.Result.UpstreamBodies) > 0 {
				withEcho++
			}
		}
		if streamed > 0 {
			out = append(out, fmt.Sprintf("**echo_upstream_body on %s**: upstream body present on %d/%d streamed successes",
				d, withEcho, streamed))
		}
	}
	var bufEcho int
	for _, o := range data.Outcomes {
		if !o.Streamed && o.Result != nil && len(o.Result.UpstreamBodies) > 0 {
			bufEcho++
		}
	}
	out = append(out, fmt.Sprintf("**echo without streaming**: %d buffered responses carried an upstream body (docs say it should be 0)", bufEcho))

	// error_type stability across dialects.
	stable, unstable, absent := errorTypeStability(data)
	if stable+unstable+absent > 0 {
		out = append(out, fmt.Sprintf(
			"**error_type \"stable across all API formats\"**: held for %d probe(s), broke for %d, absent from every dialect for %d",
			stable, unstable, absent))
		var paths []string
		for _, d := range dialects {
			if m, ok := errorTypePaths(data)[d]; ok {
				paths = append(paths, fmt.Sprintf("%s: %s", d, joinCounts(m)))
			}
		}
		if len(paths) > 0 {
			out = append(out, "**where error_type actually lives**: "+strings.Join(paths, " | "))
		}
	}

	// Router metadata coverage.
	for _, d := range dialects {
		var with, total int
		for _, o := range data.Outcomes {
			if o.Dialect != d || o.Result == nil {
				continue
			}
			total++
			if len(o.Result.RouterMetadata) > 0 {
				with++
			}
		}
		if total > 0 {
			out = append(out, fmt.Sprintf("**openrouter_metadata on %s**: present on %d/%d successes", d, with, total))
		}
	}
	return out
}

func verdict(ok, total int) string {
	switch {
	case ok == total:
		return "it works"
	case ok == 0:
		return "it does not work"
	default:
		return "mixed — see the capability matrix"
	}
}

// ---- drift ----

func driftSection(data *runner.RunData) string {
	type key struct{ entry, model string }
	bodies := map[key]map[string][]byte{}
	for _, o := range data.Outcomes {
		if o.Result == nil || len(o.Result.UpstreamBodies) == 0 || o.Kind == "error_probe" {
			continue
		}
		k := key{o.EntryID, o.Model}
		if bodies[k] == nil {
			bodies[k] = map[string][]byte{}
		}
		if _, seen := bodies[k][o.Dialect]; !seen {
			bodies[k][o.Dialect] = o.Result.UpstreamBodies[0]
		}
	}
	if len(bodies) == 0 {
		return "No upstream bodies captured (echo disabled, unsupported, or nothing streamed).\n"
	}

	keys := make([]key, 0, len(bodies))
	for k := range bodies {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].entry != keys[j].entry {
			return keys[i].entry < keys[j].entry
		}
		return keys[i].model < keys[j].model
	})

	var b strings.Builder
	b.WriteString("| entry | model | pair | verdict | top differences |\n|---|---|---|---|---|\n")
	for _, k := range keys {
		set := bodies[k]
		pairs := [][2]string{{"chat", "responses"}, {"chat", "messages"}, {"responses", "messages"}}
		for _, p := range pairs {
			a, aok := set[p[0]]
			c, cok := set[p[1]]
			if !aok || !cok {
				continue
			}
			cls := Classify(a, c)
			detail := ""
			if cls == "divergent" {
				diffs, err := DiffJSON(a, c)
				if err == nil {
					var paths []string
					for i, d := range diffs {
						if i >= 6 {
							paths = append(paths, fmt.Sprintf("… %d more", len(diffs)-i))
							break
						}
						paths = append(paths, fmt.Sprintf("`%s` (%s)", d.Path, d.Kind))
					}
					detail = strings.Join(paths, ", ")
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %s↔%s | %s | %s |\n", k.entry, k.model, p[0], p[1], cls, detail)
		}
	}
	return b.String()
}

// ---- parameter fates ----

// canonical parameter → candidate wire keys, searched recursively in the
// echoed upstream body. Provider payloads nest (generationConfig etc.), so a
// recursive search is the honest test for "did it survive".
var paramWireKeys = []struct {
	Name      string
	SpecField string // BuildNotes key for build-time unexpressibility
	Keys      []string
}{
	{"temperature", "Temperature", []string{"temperature"}},
	{"top_p", "TopP", []string{"top_p", "topP"}},
	{"top_k", "TopK", []string{"top_k", "topK"}},
	{"seed", "Seed", []string{"seed"}},
	{"max_tokens", "MaxTokens", []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxOutputTokens", "max_new_tokens"}},
	{"stop", "StopSequences", []string{"stop", "stop_sequences", "stopSequences"}},
	{"reasoning", "ReasoningEffort", []string{"reasoning", "thinking", "reasoning_effort", "reasoningEffort"}},
	{"session_id", "SessionID", []string{"session_id"}},
	{"metadata", "Metadata", []string{"metadata"}},
	{"output_config", "OutputConfig", []string{"output_config"}},
}

func fateSection(data *runner.RunData) string {
	var b strings.Builder
	b.WriteString("Fate of each canonical parameter, judged from the request body, BuildNotes, and the echoed upstream body. `no-echo` means no upstream body was available to check.\n\n")
	b.WriteString("| param | " + strings.Join(dialects, " | ") + " | catalog says |\n|---|---|---|---|---|\n")

	for _, p := range paramWireKeys {
		cells := map[string]map[string]int{}
		for _, d := range dialects {
			cells[d] = map[string]int{}
		}
		for _, o := range data.Outcomes {
			if o.Kind == "error_probe" {
				continue
			}
			f, sent := paramFate(o, p.SpecField, p.Keys)
			if sent {
				cells[o.Dialect][f]++
			}
		}
		row := make([]string, 0, len(dialects))
		any := false
		for _, d := range dialects {
			if len(cells[d]) == 0 {
				row = append(row, "not sent")
				continue
			}
			any = true
			row = append(row, joinCounts(cells[d]))
		}
		if !any {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", p.Name, strings.Join(row, " | "), catalogSupport(data, p.Name))
	}
	return b.String()
}

// paramFate classifies one parameter on one outcome; sent reports whether
// the request carried it at all.
func paramFate(o runner.Outcome, specField string, wireKeys []string) (fate string, sent bool) {
	sentKey, _, inRequest := findKey(o.RequestBody, wireKeys)
	if _, unexpr := o.Notes.Unexpressible[specField]; unexpr {
		return "unexpressible", true
	}
	if !inRequest {
		return "", false
	}
	switch {
	case o.APIError != nil:
		return fmt.Sprintf("rejected(%d %s)", o.Status, orDash(o.APIError.ErrorType)), true
	case o.TransportErr != "":
		return "unknown(transport)", true
	case o.Result == nil || len(o.Result.UpstreamBodies) == 0:
		return "no-echo", true
	}
	gotKey, _, found := findKey(o.Result.UpstreamBodies[0], wireKeys)
	switch {
	case !found:
		return "dropped", true
	case gotKey != sentKey:
		return fmt.Sprintf("renamed(%s→%s)", sentKey, gotKey), true
	default:
		return "passed", true
	}
}

func catalogSupport(data *runner.RunData, param string) string {
	if len(data.Catalog) == 0 {
		return "–"
	}
	var with, total int
	for _, m := range data.Catalog {
		total++
		for _, p := range m.SupportedParameters {
			if p == param {
				with++
				break
			}
		}
	}
	return fmt.Sprintf("%d/%d models", with, total)
}

// ---- error taxonomy ----

func taxonomySection(data *runner.RunData) string {
	var rows []runner.Outcome
	for _, o := range data.Outcomes {
		if o.APIError != nil {
			rows = append(rows, o)
		}
	}
	if len(rows) == 0 {
		return "No error envelopes observed.\n"
	}
	var b strings.Builder
	b.WriteString("| entry | model | dialect | status | error_type | found at | native type | provider_code | envelope keys |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, o := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s | %s | %s | %s |\n",
			o.EntryID, o.Model, o.Dialect, o.Status,
			orDash(o.APIError.ErrorType), orDash(o.APIError.ErrorTypePath),
			orDash(o.APIError.NativeType), orDash(o.APIError.ProviderCode),
			topKeys(o.APIError.Raw))
	}
	stable, unstable, absent := errorTypeStability(data)
	fmt.Fprintf(&b, "\nerror_type agreement across dialects for the same probe: %d consistent, %d inconsistent, %d absent everywhere.\n",
		stable, unstable, absent)
	return b.String()
}

// errorTypeStability judges the documented promise that error_type is
// "stable across all API formats". A probe only counts as holding when every
// dialect actually surfaced the same non-empty value. Absent-everywhere is
// reported separately: agreeing on nothing is not the promise being kept.
func errorTypeStability(data *runner.RunData) (stable, unstable, absent int) {
	types := map[string]map[string]bool{} // entry/model → set of error_types
	for _, o := range data.Outcomes {
		if o.APIError == nil {
			continue
		}
		k := o.EntryID + "/" + o.Model
		if types[k] == nil {
			types[k] = map[string]bool{}
		}
		types[k][o.APIError.ErrorType] = true
	}
	for _, set := range types {
		switch {
		case len(set) == 1 && set[""]:
			absent++
		case len(set) == 1:
			stable++
		default:
			unstable++
		}
	}
	return stable, unstable, absent
}

// errorTypePaths reports where error_type was found, per dialect. The
// location differs between dialects, which the docs do not mention.
func errorTypePaths(data *runner.RunData) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, o := range data.Outcomes {
		if o.APIError == nil {
			continue
		}
		if out[o.Dialect] == nil {
			out[o.Dialect] = map[string]int{}
		}
		path := o.APIError.ErrorTypePath
		if path == "" {
			path = "(absent)"
		}
		out[o.Dialect][path]++
	}
	return out
}

// ---- ledger ----

func ledgerSection(data *runner.RunData) string {
	var b strings.Builder
	b.WriteString("| entry | model | dialect | ttfb | total | tokens in/out (inline) | native in/out (reconciled) | cost inline/reconciled | note |\n|---|---|---|---|---|---|---|---|---|\n")
	n := 0
	for _, o := range data.Outcomes {
		if o.Result == nil {
			continue
		}
		n++
		r := o.Result
		inline := fmt.Sprintf("%s/%s", intPtr(r.Usage.PromptTokens), intPtr(r.Usage.CompletionTokens))
		native, recCost, note := "–", "–", ""
		if g := r.Reconciled; g != nil {
			native = fmt.Sprintf("%s/%s", intPtr(g.NativeTokensPrompt), intPtr(g.NativeTokensCompletion))
			recCost = floatPtr(g.TotalCost)
			if r.Usage.PromptTokens != nil && g.TokensPrompt != nil && *r.Usage.PromptTokens != *g.TokensPrompt {
				note = fmt.Sprintf("⚠️ inline prompt %d ≠ reconciled %d", *r.Usage.PromptTokens, *g.TokensPrompt)
			}
		}
		ttfb := "–"
		if _, unsup := r.Unsupported["TTFB"]; !unsup {
			ttfb = r.TTFB.Round(time.Millisecond).String()
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s/%s | %s |\n",
			o.EntryID, o.Model, o.Dialect, ttfb, o.Total.Round(time.Millisecond),
			inline, native, floatPtr(r.Usage.Cost), recCost, note)
	}
	if n == 0 {
		return "No successful results to account for.\n"
	}
	return b.String()
}

// ---- streamed vs buffered ----

func bufferedSection(data *runner.RunData) string {
	type key struct{ model, dialect string }
	streamed := map[key]*runner.Outcome{}
	buffered := map[key]*runner.Outcome{}
	for i := range data.Outcomes {
		o := &data.Outcomes[i]
		if o.Result == nil {
			continue
		}
		switch o.Kind {
		case "plain":
			streamed[key{o.Model, o.Dialect}] = o
		case "buffered":
			buffered[key{o.Model, o.Dialect}] = o
		}
	}
	if len(buffered) == 0 {
		return "No buffered outcomes to compare (suite has no `buffered` entry or all failed).\n"
	}
	var b strings.Builder
	b.WriteString("Same prompt, streamed (`plain`) vs buffered (`buffered`):\n\n")
	b.WriteString("| model | dialect | text match | echo absent in buffered | inline usage match |\n|---|---|---|---|---|\n")
	keys := make([]key, 0, len(buffered))
	for k := range buffered {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].dialect < keys[j].dialect
	})
	for _, k := range keys {
		bo := buffered[k]
		so, ok := streamed[k]
		if !ok {
			continue
		}
		textMatch := "✅"
		if so.Result.Text != bo.Result.Text {
			textMatch = "❌"
		}
		echoAbsent := "✅ (as documented)"
		if len(bo.Result.UpstreamBodies) > 0 {
			echoAbsent = "❌ echo present unstreamed — headline finding"
		}
		usage := "✅"
		if intPtr(so.Result.Usage.PromptTokens) != intPtr(bo.Result.Usage.PromptTokens) {
			usage = fmt.Sprintf("prompt %s vs %s", intPtr(so.Result.Usage.PromptTokens), intPtr(bo.Result.Usage.PromptTokens))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", k.model, k.dialect, textMatch, echoAbsent, usage)
	}
	return b.String()
}

// ---- determinism ----

func varianceSection(data *runner.RunData) string {
	type key struct{ entry, model, dialect string }
	texts := map[key]map[string]int{}
	for _, o := range data.Outcomes {
		if o.Result == nil || o.Attempt == 0 {
			continue
		}
		k := key{o.EntryID, o.Model, o.Dialect}
		if texts[k] == nil {
			texts[k] = map[string]int{}
		}
		texts[k][o.Result.Text]++
	}
	multi := false
	for _, m := range texts {
		total := 0
		for _, c := range m {
			total += c
		}
		if total > 1 {
			multi = true
			break
		}
	}
	if !multi {
		return ""
	}
	var b strings.Builder
	b.WriteString("| entry | model | dialect | attempts | distinct outputs |\n|---|---|---|---|---|\n")
	keys := make([]key, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.entry != c.entry {
			return a.entry < c.entry
		}
		if a.model != c.model {
			return a.model < c.model
		}
		return a.dialect < c.dialect
	})
	for _, k := range keys {
		total := 0
		for _, c := range texts[k] {
			total += c
		}
		if total < 2 {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d |\n", k.entry, k.model, k.dialect, total, len(texts[k]))
	}
	return b.String()
}

// ---- helpers ----

func entryIDs(data *runner.RunData) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range data.Outcomes {
		if !seen[o.EntryID] {
			seen[o.EntryID] = true
			out = append(out, o.EntryID)
		}
	}
	sort.Strings(out)
	return out
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, "; ")
}

func topKeys(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "(non-JSON)"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func intPtr(p *int) string {
	if p == nil {
		return "–"
	}
	return fmt.Sprintf("%d", *p)
}

func floatPtr(p *float64) string {
	if p == nil {
		return "–"
	}
	return fmt.Sprintf("%g", *p)
}
