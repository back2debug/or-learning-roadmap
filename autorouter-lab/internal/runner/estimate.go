package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"

	"github.com/back2debug/or-learning-roadmap/autorouter-lab/internal/config"
)

// CatalogURL is the public model list, used only for its prices.
const CatalogURL = "https://openrouter.ai/api/v1/models"

// Catalog holds the per-token prices of every priced model, sorted.
type Catalog struct {
	prompt     []float64
	completion []float64
}

// FetchCatalog downloads the public model list. It needs no API key.
func FetchCatalog(ctx context.Context, hc *http.Client, url string) (cat Catalog, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Catalog{}, fmt.Errorf("catalog: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Catalog{}, fmt.Errorf("catalog: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("catalog: %w", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return Catalog{}, fmt.Errorf("catalog: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Catalog{}, fmt.Errorf("catalog: %w", err)
	}
	return ParseCatalog(body)
}

// ParseCatalog reads prices out of a model list, skipping any entry it cannot
// make sense of (routers, for instance, are priced at -1).
func ParseCatalog(body []byte) (Catalog, error) {
	var doc struct {
		Data []struct {
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Catalog{}, fmt.Errorf("catalog: %w", err)
	}
	var c Catalog
	for _, m := range doc.Data {
		p, perr := strconv.ParseFloat(m.Pricing.Prompt, 64)
		q, qerr := strconv.ParseFloat(m.Pricing.Completion, 64)
		if perr != nil || qerr != nil || p < 0 || q < 0 || math.IsNaN(p+q) || math.IsInf(p+q, 0) {
			continue
		}
		c.prompt = append(c.prompt, p)
		c.completion = append(c.completion, q)
	}
	if len(c.completion) == 0 {
		return Catalog{}, errors.New("catalog: no priced models found")
	}
	slices.Sort(c.prompt)
	slices.Sort(c.completion)
	return c, nil
}

func percentile(sorted []float64, pct int) float64 {
	i := int(math.Ceil(float64(pct)/100*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// ceilingPercentile says how far up the price range a cell is allowed to
// reach. It follows the documented meaning of each setting and rounds towards
// expensive wherever that meaning is uncertain.
func ceilingPercentile(p *config.Plugin) int {
	switch {
	case p == nil:
		return 20 // no cost setting is documented to route roughly as "low"
	case len(p.AllowedModels) > 0:
		return 100 // an allow-list could name only expensive models
	case p.CostTier != nil:
		// Bands are 20 points wide; the top of the band is the ceiling.
		if i := slices.Index(config.CostTiers, *p.CostTier); i >= 0 {
			return 20 * (i + 1)
		}
		return 100
	case p.CostQualityTradeoff != nil:
		// 10 keeps roughly the cheapest 10th percentile, 0 allows up to
		// the 90th.
		return 90 - 8*(*p.CostQualityTradeoff)
	default:
		return 20
	}
}

// TrialCeiling estimates the most one trial of a cell should cost in USD.
//
// It is a planning figure, not a guarantee: the router's bands are percentiles
// of average cost per generation among models ranked for the task, and this
// uses percentiles of list price across the whole catalog, bounded by the
// cell's max_price cap when it has one. The enforced limit
// is the runner's spend cap, which counts what the API actually reports.
func (c Catalog) TrialCeiling(cell config.Cell, prompt config.Prompt) float64 {
	pct := ceilingPercentile(cell.Experiment.Plugin)
	promptPrice, completionPrice := percentile(c.prompt, pct), percentile(c.completion, pct)
	// A max_price cap bounds what any endpoint may charge, so it bounds the
	// estimate too. The cap is per million tokens; catalog prices are per token.
	if p := cell.Experiment.Provider; p != nil && p.MaxPrice != nil {
		if v := p.MaxPrice.Prompt; v != nil {
			promptPrice = min(promptPrice, *v/1e6)
		}
		if v := p.MaxPrice.Completion; v != nil {
			completionPrice = min(completionPrice, *v/1e6)
		}
	}
	promptTokens := float64(len(prompt.Text))/3 + 20
	return promptTokens*promptPrice + float64(cell.MaxTokens)*completionPrice
}
