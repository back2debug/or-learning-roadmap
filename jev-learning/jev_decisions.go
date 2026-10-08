// jev_decisions.go — three learning requests against TypeSafe: Jev on OpenRouter.
//
// Jev is a "System One" structured decision model: modality text->decisions.
// It does NOT use /chat/completions. It uses the alpha Decisions router:
//
//	POST https://openrouter.ai/api/alpha/decisions
//
// Request shape (DecisionsRequest):
//
//	model      string                 (required) e.g. "typesafe/jev-1.13" or "~typesafe/jev-latest"
//	state      string | object | array (required) the content to evaluate
//	questions  map[name]Question       your own names -> a noul/choice/score question
//	session_id string                  optional, groups related requests in the Logs view
//	user       string                  optional, end-user attribution
//
// Response shape (DecisionsResponse):
//
//	id, model, provider string
//	answers map[name]Answer  keyed by the SAME names you sent in questions
//	usage   {input_tokens, output_tokens, cost}
//
// The three question types, and what each returns:
//
//	noul   -> {"type":"noul","noul":0.96}                     a graded boolean (0..1), not a bare true/false
//	choice -> {"type":"choice","choice":"billing",...}        one key from your criteria map, + probabilities
//	score  -> {"type":"score","score":3,...}                  an ordinal position over your ordered criteria
//
// Run:  OPENROUTER_API_KEY=sk-or-v1-... go run jev_decisions.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"
)

const endpoint = "https://openrouter.ai/api/alpha/decisions"

// ---------- request types ----------

// Guidance is any of: a plain string, a JSON object, or a JSON array.
// The API accepts all three anywhere guidance or state is expected.
type Guidance = any

type Request struct {
	Model     string              `json:"model"`
	State     Guidance            `json:"state"`
	Questions map[string]Question `json:"questions,omitempty"`
	SessionID string              `json:"session_id,omitempty"`
	User      string              `json:"user,omitempty"`
}

// Question is the discriminated union of the three question types.
type Question interface{ isQuestion() }

// NoulQuestion asks for a graded boolean. criteria is optional but sharpens it.
type NoulQuestion struct {
	Type         string        `json:"type"` // always "noul"
	Instructions Guidance      `json:"instructions"`
	Criteria     *NoulCriteria `json:"criteria,omitempty"`
}
type NoulCriteria struct {
	True  Guidance `json:"true"`
	False Guidance `json:"false"`
}

// ChoiceQuestion asks Jev to pick one key from Criteria.
type ChoiceQuestion struct {
	Type         string              `json:"type"` // always "choice"
	Instructions Guidance            `json:"instructions"`
	Criteria     map[string]Guidance `json:"criteria"`
}

// ScoreQuestion asks for a position on an ORDERED ladder. Order matters:
// element 0 is the low end, the last element is the high end.
type ScoreQuestion struct {
	Type         string     `json:"type"` // always "score"
	Instructions Guidance   `json:"instructions"`
	Criteria     []Guidance `json:"criteria"`
}

func (NoulQuestion) isQuestion()   {}
func (ChoiceQuestion) isQuestion() {}
func (ScoreQuestion) isQuestion()  {}

func Noul(instructions string, yes, no string) NoulQuestion {
	return NoulQuestion{Type: "noul", Instructions: instructions,
		Criteria: &NoulCriteria{True: yes, False: no}}
}
func Choice(instructions string, criteria map[string]Guidance) ChoiceQuestion {
	return ChoiceQuestion{Type: "choice", Instructions: instructions, Criteria: criteria}
}
func Score(instructions string, ladder ...Guidance) ScoreQuestion {
	return ScoreQuestion{Type: "score", Instructions: instructions, Criteria: ladder}
}

// ---------- response types ----------

type Response struct {
	ID       string                     `json:"id"`
	Model    string                     `json:"model"`
	Provider string                     `json:"provider"`
	Answers  map[string]json.RawMessage `json:"answers"`
	Usage    Usage                      `json:"usage"`
}

type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Cost         float64 `json:"cost"`
}

type NoulAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"` // 0..1 — how true, not whether true
}

type ChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type ScoreAnswer struct {
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Legend        map[string]any     `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type APIError struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// Decode turns one raw answer into its concrete type by switching on "type".
func Decode(raw json.RawMessage) (any, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case "noul":
		var a NoulAnswer
		return a, json.Unmarshal(raw, &a)
	case "choice":
		var a ChoiceAnswer
		return a, json.Unmarshal(raw, &a)
	case "score":
		var a ScoreAnswer
		return a, json.Unmarshal(raw, &a)
	default:
		return nil, fmt.Errorf("unknown answer type %q", probe.Type)
	}
}

// ---------- transport ----------

func post(req Request, lg *log.Logger) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	pretty, _ := json.MarshalIndent(req, "", "  ")
	lg.Printf("REQUEST -> POST %s\n%s", endpoint, pretty)

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+os.Getenv("OPENROUTER_API_KEY"))
	httpReq.Header.Set("Content-Type", "application/json")
	// Optional attribution headers; they show up on the OpenRouter activity page.
	httpReq.Header.Set("HTTP-Referer", "https://localhost/jev-learning")
	httpReq.Header.Set("X-Title", "Jev Decisions Learning")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	lg.Printf("RESPONSE <- %s in %s\n%s", resp.Status, time.Since(start).Round(time.Millisecond), raw)

	if resp.StatusCode != http.StatusOK {
		var e APIError
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s (code %d)", e.Error.Message, e.Error.Code)
		}
		return nil, fmt.Errorf("http %s: %s", resp.Status, raw)
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- the three learning requests ----------

// 1. The smallest useful call: one graded boolean over a plain-string state.
func requestOne(lg *log.Logger) (*Response, error) {
	return post(Request{
		Model: "typesafe/jev-1.13",
		State: "The checkout page has returned HTTP 500 for every user since the 14:20 deploy. " +
			"Card payments are failing. Three customers have emailed in the last ten minutes.",
		Questions: map[string]Question{
			"is_urgent": Noul(
				"Does this incident require paging the on-call engineer right now?",
				"Revenue or a core user flow is broken in production for many users.",
				"Cosmetic, low-traffic, already mitigated, or affecting only a handful of users.",
			),
		},
	}, lg)
}

//  2. Routing: pick one key from a criteria map. State is a JSON object, which
//     lets Jev see fields separately instead of as one blob of prose.
func requestTwo(lg *log.Logger) (*Response, error) {
	return post(Request{
		Model: "typesafe/jev-1.13",
		State: map[string]any{
			"subject": "Charged twice for the September invoice",
			"body": "My card was billed $49 on Sept 2 and again on Sept 3 for what looks like " +
				"the same invoice. I only have one active subscription. Please refund the duplicate.",
			"customer_plan":    "pro",
			"attachments":      []string{"receipt_sept2.pdf", "receipt_sept3.pdf"},
			"previous_tickets": 0,
		},
		Questions: map[string]Question{
			"queue": Choice(
				"Which team should own this ticket?",
				map[string]Guidance{
					"billing":   "Charges, refunds, invoices, proration, payment methods.",
					"technical": "Bugs, errors, outages, API or integration problems.",
					"account":   "Login, SSO, seats, permissions, plan changes.",
					"abuse":     "Spam, fraud, policy violations, or security reports.",
				},
			),
		},
		SessionID: "jev-learning-support-triage",
		User:      "learning-script",
	}, lg)
}

//  3. Several questions in one round trip. The answers map comes back keyed by
//     the same names you chose, so one call can drive a whole triage decision.
//     Note the score ladder is ORDERED low -> high.
func requestThree(lg *log.Logger) (*Response, error) {
	return post(Request{
		Model: "~typesafe/jev-latest", // alias: always the newest model in the Jev family
		State: map[string]any{
			"pull_request": "Add retry with exponential backoff to the payments client",
			"files_changed": []string{
				"internal/payments/client.go",
				"internal/payments/client_test.go",
			},
			"additions":            142,
			"deletions":            12,
			"touches_auth":         false,
			"touches_money":        true,
			"has_tests":            true,
			"ci_status":            "passing",
			"author_tenure_months": 2,
		},
		Questions: map[string]Question{
			"risk": Score(
				"How much review scrutiny does this change need?",
				"Trivial: docs, comments, or formatting only.",
				"Low: isolated change with tests, no production data at stake.",
				"Moderate: touches a live subsystem but is well covered by tests.",
				"High: touches money, auth, or data integrity.",
				"Critical: irreversible or customer-visible failure if wrong.",
			),
			"needs_second_reviewer": Noul(
				"Should this pull request require a second approving reviewer?",
				"Touches money, auth, or data integrity, or the author is new to the codebase.",
				"Routine change in a well-tested area by an experienced author.",
			),
			"area": Choice(
				"Which part of the system does this change primarily affect?",
				map[string]Guidance{
					"payments":       "Billing, charges, refunds, payment providers.",
					"auth":           "Login, sessions, tokens, permissions.",
					"infrastructure": "Deploys, CI, observability, configuration.",
					"frontend":       "UI components, styling, client-side behaviour.",
				},
			),
		},
		SessionID: "jev-learning-pr-triage",
		User:      "learning-script",
	}, lg)
}

// ---------- reporting ----------

func report(title string, resp *Response) {
	fmt.Printf("\n=== %s\n", title)
	fmt.Printf("    %s via %s  (id %s)\n", resp.Model, resp.Provider, resp.ID)
	for name, raw := range resp.Answers {
		ans, err := Decode(raw)
		if err != nil {
			fmt.Printf("    %-22s <decode error: %v>\n", name, err)
			continue
		}
		switch a := ans.(type) {
		case NoulAnswer:
			fmt.Printf("    %-22s noul   %.2f  -> %v\n", name, a.Noul, a.Noul >= 0.5)
		case ChoiceAnswer:
			fmt.Printf("    %-22s choice %q (confidence %.2f)\n", name, a.Choice, a.Confidence)
			for k, p := range a.Probabilities {
				fmt.Printf("    %-22s          %-16s %.3f\n", "", k, p)
			}
		case ScoreAnswer:
			// Score is continuous, not an integer index: 2.92 means "just under
			// rung 3". Legend maps each rung index back to the criteria text.
			fmt.Printf("    %-22s score  %.2f (confidence %.2f)\n", name, a.Score, a.Confidence)
			if label, ok := a.Legend[strconv.Itoa(int(math.Round(a.Score)))]; ok {
				fmt.Printf("    %-22s          nearest rung: %v\n", "", label)
			}
			for k, p := range a.Probabilities {
				fmt.Printf("    %-22s          rung %-11s %.3f\n", "", k, p)
			}
		}
	}
	fmt.Printf("    tokens in/out %d/%d  cost $%.6f\n",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.Cost)
}

func main() {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		log.Fatal("OPENROUTER_API_KEY is not set")
	}

	logPath := fmt.Sprintf("jev_decisions_%s.log", time.Now().Format("20060102_150405"))
	f, err := os.Create(logPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	lg := log.New(f, "", log.LstdFlags|log.Lmicroseconds)

	steps := []struct {
		title string
		fn    func(*log.Logger) (*Response, error)
	}{
		{"1. noul — graded boolean over a string state", requestOne},
		{"2. choice — route a ticket, structured object state", requestTwo},
		{"3. score + noul + choice — three questions, one round trip", requestThree},
	}

	var total float64
	for _, s := range steps {
		lg.Printf("---------- %s ----------", s.title)
		resp, err := s.fn(lg)
		if err != nil {
			fmt.Printf("\n=== %s\n    FAILED: %v\n", s.title, err)
			lg.Printf("ERROR: %v", err)
			continue
		}
		report(s.title, resp)
		total += resp.Usage.Cost
	}

	fmt.Printf("\ntotal cost $%.6f\nfull request/response log: %s\n", total, logPath)
	lg.Printf("total cost $%.6f", total)
}
