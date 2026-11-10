# Definition of Done

A prompt is complete when ALL of the following are true:

## Build

- [ ] `make precommit` passes (gofmt + go vet + go test)

## Code Quality

- [ ] No `# type: ignore` without explanation
- [ ] No `noqa` without explanation

## Tests

- [ ] New functions have tests
- [ ] Existing tests still pass
- [ ] Go tests use the standard `testing` package

## Style

- [ ] Follows existing code patterns in the file being modified
- [ ] Functions over classes for stateless operations (see `internal/`)
- [ ] No absolute paths — all paths relative or using `Path`
