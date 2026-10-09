# or-learning-roadmap

Learning projects for the OpenRouter API. Each directory is self-contained and
documented in its own README.

| Project | Language | What it is |
| --- | --- | --- |
| [sdk-examples](sdk-examples/README.md) | Python | Annotated SDK examples and a model catalog browser covering six modalities |
| [jev-learning](jev-learning/README.md) | Go | Three requests against the TypeSafe: Jev decision model |
| [orlab](orlab/README.md) | Go | Sends one prompt through all three API dialects (Chat Completions, Responses, Messages) and compares the results |
| [pareto-lab](pareto-lab/README.md) | Go | Observes which model the Pareto Code router picks, with cost, latency, and token usage |
| [reasoning-explorer](reasoning-explorer/README.md) | Go | Dissects reasoning models over raw HTTP, in six phases |
| [tool-calling](tool-calling/README.md) | Go | Tool calling with a pinned model versus the two auto routers |

Every project reads the API key from the `OPENROUTER_API_KEY` environment
variable, and most make real, billed API calls when run.

## Layout

Each Go project is its own module with its own `go.mod`, so run `go` commands
from inside the project directory:

```bash
cd pareto-lab
go run .
```

The Python scripts share one `requirements.txt` in `sdk-examples/`.

## CI

- `.github/workflows/orlab-ci.yml` checks `orlab`.
- `.github/workflows/tutorials-ci.yml` runs build, vet, tests, govulncheck, and
  gofmt for each of the other Go modules.
