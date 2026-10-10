package assert

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// Finding is a conclusion drawn from comparing cells, which no single trial
// can support.
type Finding struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	// Group is the comparison scope: router, transport and prompt.
	Group string `json:"group"`
	// Cells names the cell the finding is about first, then what it was
	// compared with.
	Cells  []string `json:"cells"`
	Detail string   `json:"detail"`
}

// Cell is the clean trials of one cell, with the settings they share.
type Cell struct {
	ID        string
	Router    string
	Transport string
	PromptID  string
	Plugin    *config.Plugin
	Provider  *config.Provider
	Trials    []Trial
}

// Setting describes the cell's routing config in a few words.
func (c Cell) Setting() string { return Describe(c.Plugin, c.Provider) }

// Describe renders a routing config compactly, for tables.
func Describe(p *config.Plugin, prov *config.Provider) string {
	var parts []string
	if p != nil {
		if p.CostTier != nil {
			parts = append(parts, "tier="+string(*p.CostTier))
		}
		if p.CostQualityTradeoff != nil {
			parts = append(parts, fmt.Sprintf("dial=%d", *p.CostQualityTradeoff))
		}
		if len(p.AllowedModels) > 0 {
			parts = append(parts, "allow="+strings.Join(p.AllowedModels, ","))
		}
		if len(p.ExcludedModels) > 0 {
			parts = append(parts, "exclude="+strings.Join(p.ExcludedModels, ","))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "no cost setting")
	}
	if prov != nil && prov.MaxPrice != nil {
		price := func(v *float64) string {
			if v == nil {
				return "-"
			}
			return fmt.Sprintf("%g", *v)
		}
		parts = append(parts, "max_price="+price(prov.MaxPrice.Prompt)+"/"+price(prov.MaxPrice.Completion))
	}
	return strings.Join(parts, " ")
}

// Baseline returns the unconfigured cell to compare c against: same scope,
// no plugin block, and the same provider block, so that a price cap sent on
// both sides cancels out. It returns nil when there is none.
func Baseline(c Cell, cells []Cell) *Cell {
	for _, b := range cells {
		if b.Plugin == nil && b.ID != c.ID && b.Scope() == c.Scope() && reflect.DeepEqual(b.Provider, c.Provider) {
			return &b
		}
	}
	return nil
}

// Scope is what two cells must share before their choices are comparable at
// all: router, transport and prompt.
func (c Cell) Scope() string { return c.Router + " / " + c.Transport + " / " + c.PromptID }

// group is the scope plus the provider block. Cells are only compared across
// cost settings when they sent the same provider block, so that a price cap
// present on one side cannot be mistaken for the effect of the setting.
func (c Cell) group() string {
	g := c.Scope()
	if c.Provider != nil {
		g += " / " + Describe(nil, c.Provider)[len("no cost setting "):]
	}
	return g
}

// Group is the comparison group: scope plus provider block.
func (c Cell) Group() string { return c.group() }

// SameDecisions reports whether two cells made the same set of choices.
func SameDecisions(a, b Cell) bool { return sameDecisions(a, b) }

// Decisions returns the cell's distinct router choices, sorted.
func (c Cell) Decisions() []string {
	var out []string
	for _, t := range c.Trials {
		if d := t.Decision(); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// MeanCost is the average reported cost of the cell's trials.
func (c Cell) MeanCost() float64 {
	if len(c.Trials) == 0 {
		return 0
	}
	sum := 0.0
	for _, t := range c.Trials {
		sum += t.CostUSD
	}
	return sum / float64(len(c.Trials))
}

func sameDecisions(a, b Cell) bool { return slices.Equal(a.Decisions(), b.Decisions()) }

// Cells groups trials into cells, keeping only what cross-cell comparison can
// use: clean trials from isolate cells. Sticky and implicit cells are left
// out on purpose, since their trials are not independent routing decisions.
func Cells(trials []Trial) []Cell {
	var cells []Cell
	index := map[string]int{}
	for _, t := range trials {
		if t.Stickiness != string(config.StickinessIsolate) || !Clean(t) {
			continue
		}
		i, ok := index[t.CellID]
		if !ok {
			i = len(cells)
			index[t.CellID] = i
			cells = append(cells, Cell{ID: t.CellID, Router: t.Router, Transport: t.Transport, PromptID: t.PromptID, Plugin: t.Plugin, Provider: t.ProviderConfig})
		}
		cells[i].Trials = append(cells[i].Trials, t)
	}
	return cells
}

type costSetting struct {
	tier *config.CostTier
	dial *int
}

func setting(p *config.Plugin) (costSetting, bool) {
	if p == nil {
		return costSetting{}, true
	}
	// Cells that also restrict models are not comparable on cost alone.
	if len(p.AllowedModels) > 0 || len(p.ExcludedModels) > 0 {
		return costSetting{}, false
	}
	return costSetting{tier: p.CostTier, dial: p.CostQualityTradeoff}, true
}

// CheckRun draws the cross-cell conclusions for one run's trials.
func CheckRun(trials []Trial) []Finding {
	groups := map[string][]Cell{}
	var order []string
	for _, c := range Cells(trials) {
		g := c.group()
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], c)
	}
	var out []Finding
	for _, g := range order {
		cells := groups[g]
		out = append(out, noEffect(g, cells)...)
		out = append(out, tierDirection(g, cells)...)
		out = append(out, precedence(g, cells)...)
	}
	return out
}

// noEffect flags every configured cell whose router choices are identical to
// the unconfigured baseline for the same router, transport and prompt.
//
// A flag is not proof the config was ignored: tier "low" matching a default
// that is documented to behave like "low" is the config working. It marks
// where the data cannot tell "honoured" from "ignored", which is what has to
// be known before reading anything into the cell.
func noEffect(group string, cells []Cell) []Finding {
	var out []Finding
	for _, c := range cells {
		if c.Plugin == nil {
			continue
		}
		b := Baseline(c, cells)
		if b == nil {
			continue
		}
		base := *b
		f := Finding{Check: "observable_effect", Group: group, Cells: []string{c.ID, base.ID}}
		if sameDecisions(c, base) {
			f.Status = Flag
			f.Detail = fmt.Sprintf("same choice as the baseline (%s); this cell cannot show the config did anything", strings.Join(base.Decisions(), ", "))
		} else {
			f.Status = Pass
			f.Detail = fmt.Sprintf("%s, baseline %s", strings.Join(c.Decisions(), ", "), strings.Join(base.Decisions(), ", "))
		}
		out = append(out, f)
	}
	return out
}

// inversionRatio is how far cost must fall between one tier and the next
// before the sweep is called inverted. Observed cost on a short completion is
// a noisy stand-in for the router's own cost measure: the same model cost
// $0.000434 in one run and $0.000360 in the next, depending on which provider
// served it. A small dip between adjacent bands is therefore inconclusive,
// and only a fall to under half is treated as a contradiction.
const inversionRatio = 0.5

// tierDirection checks the tier sweep: cost must not fall sharply as the tier
// rises, and a tier that selects a different, dearer model than the tier
// below shows the band excluding the cheaper choice as well as bounding the
// dear end.
func tierDirection(group string, cells []Cell) []Finding {
	byTier := map[config.CostTier]Cell{}
	for _, c := range cells {
		if s, ok := setting(c.Plugin); ok && s.tier != nil && s.dial == nil {
			// Two cells with the same tier in one group are replicates;
			// the first stands for the tier.
			if _, dup := byTier[*s.tier]; !dup {
				byTier[*s.tier] = c
			}
		}
	}
	var prev *Cell
	var out []Finding
	for _, tier := range config.CostTiers {
		c, ok := byTier[tier]
		if !ok {
			continue
		}
		if prev != nil {
			f := Finding{Check: "cost_tier_direction", Group: group, Cells: []string{c.ID, prev.ID}}
			detail := fmt.Sprintf("$%.6f (%s), up from %s at $%.6f (%s)",
				c.MeanCost(), strings.Join(c.Decisions(), ", "), prev.ID, prev.MeanCost(), strings.Join(prev.Decisions(), ", "))
			switch {
			case c.MeanCost() < prev.MeanCost()*inversionRatio:
				f.Status, f.Detail = Fail, "cost fell sharply as the tier rose: "+detail
			case sameDecisions(c, *prev):
				f.Status, f.Detail = Flag, "same model in both bands, so the band did not visibly move: "+detail
			case c.MeanCost() < prev.MeanCost():
				f.Status, f.Detail = Flag, "different model, but its observed cost was not higher; at this token budget cost varies with the provider that served, so this step is inconclusive: "+detail
			default:
				f.Status, f.Detail = Pass, "dearer and different, so the lower band's choice was excluded: "+detail
			}
			out = append(out, f)
		}
		prev = &c
	}
	return out
}

// precedence examines every cell that sent both cost settings, by comparing
// it with the cell that sent only its tier and the cell that sent only its
// dial value.
func precedence(group string, cells []Cell) []Finding {
	find := func(tier *config.CostTier, dial *int) *Cell {
		for _, c := range cells {
			s, ok := setting(c.Plugin)
			if !ok || c.Plugin == nil {
				continue
			}
			sameTier := (s.tier == nil) == (tier == nil) && (tier == nil || *s.tier == *tier)
			sameDial := (s.dial == nil) == (dial == nil) && (dial == nil || *s.dial == *dial)
			if sameTier && sameDial {
				return &c
			}
		}
		return nil
	}
	var out []Finding
	for _, c := range cells {
		s, ok := setting(c.Plugin)
		if !ok || s.tier == nil || s.dial == nil {
			continue
		}
		f := Finding{Check: "cost_tier_precedence", Group: group, Cells: []string{c.ID}}
		tierOnly, dialOnly := find(s.tier, nil), find(nil, s.dial)
		if tierOnly == nil || dialOnly == nil {
			f.Status, f.Detail = Skip, "needs a tier-only cell and a dial-only cell with the same values to compare against"
			out = append(out, f)
			continue
		}
		f.Cells = append(f.Cells, tierOnly.ID, dialOnly.ID)
		likeTier, likeDial := sameDecisions(c, *tierOnly), sameDecisions(c, *dialOnly)
		got := strings.Join(c.Decisions(), ", ")
		switch {
		case likeTier && likeDial:
			f.Status, f.Detail = Flag, fmt.Sprintf("tier-only and dial-only cells both choose %s, so this pair cannot separate them", got)
		case likeTier:
			f.Status, f.Detail = Pass, fmt.Sprintf("cost_tier wins: %s, as in %s; %s chose %s", got, tierOnly.ID, dialOnly.ID, strings.Join(dialOnly.Decisions(), ", "))
		case likeDial:
			f.Status, f.Detail = Fail, fmt.Sprintf("cost_quality_tradeoff wins: %s, as in %s; %s chose %s", got, dialOnly.ID, tierOnly.ID, strings.Join(tierOnly.Decisions(), ", "))
		default:
			f.Status, f.Detail = Flag, fmt.Sprintf("matches neither: %s; tier-only %s, dial-only %s", got, strings.Join(tierOnly.Decisions(), ", "), strings.Join(dialOnly.Decisions(), ", "))
		}
		out = append(out, f)
	}
	return out
}
