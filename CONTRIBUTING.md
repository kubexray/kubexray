# Contributing to kubexray

Thanks for your interest! kubexray is early-stage; see [ROADMAP.md](ROADMAP.md) for where it's heading.

## Development setup

You need Go (any recent version; the `go` command downloads the exact toolchain from `go.mod`) and Docker. For the web UI you also need Node (the version in `web/.nvmrc`).
All other Go tools (golangci-lint, govulncheck, gitleaks, actionlint, tygo) are pinned in the `Makefile` and run via `go run`, so there is nothing else to install.

```sh
make help       # list targets
make build      # build ./bin/kubexray with the web UI
make go-build   # build without rebuilding the UI (no Node needed)
make test       # Go unit tests with the race detector
make check      # the Go checks CI runs: lint, test, vuln, secrets, actionlint
make web-check  # the UI checks: types, lint, tests
make gen        # regenerate the UI's TypeScript types after changing Go types
make fmt        # format Go code
```

### Working on the UI

The UI only works behind kubexray's server (session cookie, same-origin checks), so there is no separate dev server. Instead:

```sh
cd web && npm run watch                                    # rebuilds on every change
./bin/kubexray --ui-dir internal/web/dist --context kind-…  # serves those files; reload the page
```

## Workflow

1. Open or pick an issue. For anything architectural, write an ADR in [docs/adr/](docs/adr/) first.
2. Branch from `main`, keep the change small and focused.
3. Write tests alongside the code.
4. Run `make check` before pushing.
5. Open a PR and fill in the template, including the security checklist.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(graph): add EndpointSlice edges
fix(ws): reject connections with a foreign Origin
docs(adr): record delta schema decision
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`.

## Security

kubexray handles cluster credentials. Read [docs/threat-model.md](docs/threat-model.md) before touching networking, the API surface, or anything that reads Secrets, logs or env. Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).
