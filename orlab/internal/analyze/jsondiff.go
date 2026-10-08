package analyze

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// DiffEntry is one structural difference between two JSON documents.
type DiffEntry struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // "only_a" | "only_b" | "changed"
	A    string `json:"a,omitempty"`
	B    string `json:"b,omitempty"`
}

// DiffJSON structurally compares two JSON documents, ignoring key order and
// whitespace.
func DiffJSON(a, b []byte) ([]DiffEntry, error) {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return nil, fmt.Errorf("analyze: left document: %w", err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return nil, fmt.Errorf("analyze: right document: %w", err)
	}
	var out []DiffEntry
	walkDiff("$", va, vb, &out)
	return out, nil
}

// Classify labels the relationship between two JSON documents:
// byte-identical, semantically equivalent, or divergent.
func Classify(a, b []byte) string {
	if bytes.Equal(a, b) {
		return "identical"
	}
	diffs, err := DiffJSON(a, b)
	if err != nil {
		return "unparseable"
	}
	if len(diffs) == 0 {
		return "equivalent"
	}
	return "divergent"
}

func walkDiff(path string, a, b any, out *[]DiffEntry) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			av, aok := am[k]
			bv, bok := bm[k]
			sub := path + "." + k
			switch {
			case aok && !bok:
				*out = append(*out, DiffEntry{Path: sub, Kind: "only_a", A: compact(av)})
			case !aok && bok:
				*out = append(*out, DiffEntry{Path: sub, Kind: "only_b", B: compact(bv)})
			default:
				walkDiff(sub, av, bv, out)
			}
		}
		return
	}

	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		n := max(len(as), len(bs))
		for i := 0; i < n; i++ {
			sub := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i >= len(bs):
				*out = append(*out, DiffEntry{Path: sub, Kind: "only_a", A: compact(as[i])})
			case i >= len(as):
				*out = append(*out, DiffEntry{Path: sub, Kind: "only_b", B: compact(bs[i])})
			default:
				walkDiff(sub, as[i], bs[i], out)
			}
		}
		return
	}

	if !scalarEqual(a, b) {
		*out = append(*out, DiffEntry{Path: path, Kind: "changed", A: compact(a), B: compact(b)})
	}
}

func scalarEqual(a, b any) bool {
	// Post-Unmarshal scalars are bool, float64, string, or nil; slices/maps
	// of mismatched kinds land here too and compare by re-encoding.
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	default:
		return compact(a) == compact(b)
	}
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	const maxLen = 100
	if len(b) > maxLen {
		return string(b[:maxLen]) + "…"
	}
	return string(b)
}

// findKey searches a JSON document recursively for any of the candidate
// keys, returning the first key found and its path.
func findKey(doc []byte, candidates []string) (key, path string, found bool) {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return "", "", false
	}
	want := map[string]bool{}
	for _, c := range candidates {
		want[c] = true
	}
	return searchKeys("$", v, want)
}

func searchKeys(path string, v any, want map[string]bool) (string, string, bool) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if want[k] {
				return k, path + "." + k, true
			}
		}
		for _, k := range keys {
			if k2, p2, ok := searchKeys(path+"."+k, t[k], want); ok {
				return k2, p2, true
			}
		}
	case []any:
		for i, e := range t {
			if k2, p2, ok := searchKeys(fmt.Sprintf("%s[%d]", path, i), e, want); ok {
				return k2, p2, true
			}
		}
	}
	return "", "", false
}
