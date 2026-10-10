package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReport(t *testing.T) {
	rec := func(run, cell, model string, status int, extra string) string {
		return `{"msg":"trial","run_id":"` + run + `","cell_id":"` + cell + `","router":"auto","transport":"chat_completions","stickiness":"isolate","prompt_id":"p",` +
			`"status":` + map[int]string{200: "200", 404: "404"}[status] + `,"model":"` + model + `","resolved_to":"` + model + `-1","task_type":"math","cost_usd":0.001,` +
			`"request_payload":{"model":"openrouter/auto"}` + extra + `}`
	}
	lines := []string{
		rec("old", "baseline/auto", "v/old", 200, ""),
		`not json at all`,
		`{"msg":"something else","run_id":"new"}`,
		rec("new", "baseline/auto", "v/a", 200, ""),
		rec("new", "dry/auto", "v/a", 200, `,"dry_run":true`),
		rec("new", "evil/auto", "v/<script>|x`y", 200, ""),
		rec("new", "gone/auto", "", 404, ""),
	}
	path := filepath.Join(t.TempDir(), "trials.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	render := func(runs string) (string, error) {
		trials, skipped, err := loadTrials(path)
		if err != nil {
			return "", err
		}
		trials, ids, err := selectRuns(trials, runs)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		report(&buf, ids, trials, map[string]generation{}, skipped)
		return buf.String(), nil
	}
	out, err := render("")
	if err != nil {
		t.Fatal(err)
	}
	// The latest run is the default; the dry run and junk lines are left out.
	for _, want := range []string{"Runs: new. 3 live trials", "2 log lines were not trial records", `| new | gone/auto | 0.0 | expected\_status | fail |`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "v/old") {
		t.Error("an older run leaked into the report")
	}

	if strings.Contains(out, "<script>") {
		t.Error("model-supplied markup reached the report unescaped")
	}
	if old, err := render("old"); err != nil || !strings.Contains(old, "Runs: old. 1 live trials") {
		t.Errorf("-run old: err %v, out %s", err, old)
	}
	if both, err := render("all"); err != nil || !strings.Contains(both, "Runs: old, new. 4 live trials") {
		t.Errorf("-run all: err %v", err)
	}
	if _, err := render("missing"); err == nil {
		t.Error("an unknown run id was accepted")
	}
}

// Regenerating the report keeps hand-written notes above the marker and
// refuses to clobber a file that has none.
func TestWriteBelowMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "FINDINGS.md")
	if err := writeBelowMarker(path, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	edited := strings.Replace(string(b), "# Findings\n", "# Findings\n\nMy notes.\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBelowMarker(path, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	got := string(b)
	if !strings.Contains(got, "My notes.") || !strings.Contains(got, "second") || strings.Contains(got, "first") || strings.Count(got, marker) != 1 {
		t.Errorf("regenerated file = %q", got)
	}
	plain := filepath.Join(dir, "README.md")
	if err := os.WriteFile(plain, []byte("someone's document\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBelowMarker(plain, []byte("x")); err == nil {
		t.Error("a file without the marker was overwritten")
	}
}

// Model output reaches the report as text, never as markup.
func TestMarkdownEscaping(t *testing.T) {
	got := md("a|b <script>alert(1)</script> `c`\nd\x1b[2J")
	for _, bad := range []string{"<", ">", "`", "\n", "\x1b"} {
		if strings.Contains(got, bad) {
			t.Errorf("md left %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, `a\|b`) {
		t.Errorf("pipe not escaped: %q", got)
	}
}
