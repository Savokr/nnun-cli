# Contribution guidelines

Read README.md. Preserve existing changes and keep README/these guidelines current when contracts, tooling, installation, or behavior change.

- This repository is public. Never add app source, secrets, Supabase service-role keys, or real user credentials.
- Executable and command prefix are `nnun`; display name is Nnun.
- Consume API version 1's project DTOs and immutable prepare/upload/complete contract. Backend contract changes need coordinated review in the private app's ADRs; do not invent revision, overwrite, or collaboration endpoints.
- Keep OAuth PKCE S256, random state, loopback-only registered callbacks, explicit consent, and OS credential storage. No plaintext credential fallback. Keep tokens isolated by origin, reject cross-origin redirects, and never log tokens or signed URLs.
- Do not delete a file when completion may have succeeded. Only failed pre-completion transfers attempt cancellation.
- Installers resolve one version, verify archive checksums, install per-user without sudo, and preserve safe quoting/idempotent PATH updates. Keep installer copies in the private app's public directory synchronized.
- Use `gofmt`, `go vet ./...`, `go test ./...`, and relevant installer tests. Use `go test -race ./...` for authentication/concurrency changes and cross-compile release targets when dependencies change. Never claim hosted login or CI passed without running it.
- Releases use semantic version `v*` tags and GitHub's built-in token. Do not publish a release unless the user/parent task authorized publication.
