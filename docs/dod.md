# Definition of Done

A prompt is complete when ALL of the following are true:

## Build

- [ ] `make precommit` passes (format + lint + typecheck + test)
- [ ] No new ruff warnings or errors
- [ ] No new mypy errors (strict mode)

## Code Quality

- [ ] Type hints on all function signatures
- [ ] Docstrings on all public functions (following existing style)
- [ ] No `# type: ignore` without explanation
- [ ] No `noqa` without explanation

## Tests

- [ ] New functions have tests
- [ ] Existing tests still pass
- [ ] Tests use pytest conventions (no unittest)

## Style

- [ ] Follows existing code patterns in the file being modified
- [ ] Functions over classes for stateless operations (see `git_operations.py`)
- [ ] subprocess.run with `check=False` and explicit error handling
- [ ] No absolute paths — all paths relative or using `Path`
