# Contributing

Thanks for your interest in NetPulse!

## Development setup

```bash
git clone https://github.com/agarwala-ayush/netpulse && cd netpulse
make build && ./bin/netpulse check github.com
make test      # race detector on
make lint      # requires golangci-lint
```

## Guidelines

- Keep `internal/checks` free of dependencies on config, metrics or UI packages.
- New checkers implement `checks.Checker`, never return Go errors (encode failures
  in `Result`), and must honour context cancellation.
- Tests must not depend on the internet: use `httptest`, the in-process DNS server
  and the throwaway CA helpers in `internal/checks/helpers_test.go`.
- New metric labels must have bounded values (from config or a fixed set).
- Run `gofmt`, `go vet` and the tests before opening a pull request.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`,
`docs:`, `test:`, `chore:`); the release changelog is generated from them.
