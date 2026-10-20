# Contributing to kubexray

Thanks for your interest! kubexray is early-stage; see [ROADMAP.md](ROADMAP.md) for where it's heading.

## Development setup

You need Go (any recent version; the `go` command downloads the exact toolchain from `go.mod`) and Docker.
All other tools (golangci-lint, govulncheck, gitleaks, actionlint) are pinned in the `Makefile` and run via `go run`, so there is nothing else to install.

```sh
make help    # list targets
make build   # build ./bin/kubexray
make test    # unit tests with the race detector
make check   # everything CI runs: lint, test, vuln, secrets, actionlint
make fmt     # format code
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
