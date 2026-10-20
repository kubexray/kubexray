## What

<!-- What does this change and why? Link the issue / roadmap item. -->

## How it was tested

<!-- Unit tests, envtest, kind, manual steps. -->

## Checklist

- [ ] `make check` passes locally
- [ ] Tests added or updated
- [ ] Docs / ADR updated if behaviour or architecture changed

### Security

- [ ] No new write access to the cluster (or it is behind `--allow-actions` and an RBAC check)
- [ ] No Secret values, env values or `last-applied-configuration` reach the UI, logs or replay files
- [ ] Untrusted cluster strings (names, labels, annotations) are rendered as text, never HTML
- [ ] New network listeners default to `127.0.0.1` and validate `Origin` / auth
- [ ] New dependencies are justified and actively maintained
