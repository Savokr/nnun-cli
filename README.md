# Nnun CLI

The `nnun` command signs in to [Nnun](https://v0-knowledge-graph-explorer-ten.vercel.app), manages research projects and folders, and uploads original LaTeX, text, Markdown, and PDF files. Uploading does not compile TeX or generate a mathematical graph yet.

## Install

macOS or Linux (x64 and ARM64, including WSL):

```sh
curl -fsSL https://v0-knowledge-graph-explorer-ten.vercel.app/install.sh | sh
```

Windows PowerShell (x64 and ARM64):

```powershell
irm https://v0-knowledge-graph-explorer-ten.vercel.app/install.ps1 | iex
```

These URLs become available when the website deploys the installers. You can also download and inspect `install.sh` or `install.ps1` from this repository, then execute it locally. No Node.js, Go, administrator rights, or package manager is required. The installers download a native executable from [GitHub Releases](https://github.com/Savokr/nnun-cli/releases), verify its SHA-256 checksum, and add its directory to your user PATH. Open a new terminal after installation. Linux uses static Go binaries compatible with glibc and musl; interactive login requires a Secret Service credential store and browser.

Pin a version or rerun the installer to update:

```sh
curl -fsSL https://v0-knowledge-graph-explorer-ten.vercel.app/install.sh | sh -s -- --version 0.1.0
```

```powershell
$env:NNUN_VERSION = '0.1.0'
irm https://v0-knowledge-graph-explorer-ten.vercel.app/install.ps1 | iex
```

`NNUN_VERSION` also works on macOS/Linux. `NNUN_INSTALL_DIR` overrides the absolute installation directory. Defaults are `~/.local/bin` and `%LOCALAPPDATA%\Nnun\bin`. POSIX shell PATH updates go to `.zshrc`, `.bashrc`, or `.profile`; fish users must add the installation directory to their PATH themselves. Release checksums detect corruption, but the binaries are not yet publisher-signed or notarized.

## Quick start

```sh
nnun login
nnun whoami
nnun projects create "My research"
nnun projects list
nnun use "My research"
nnun push paper.tex appendix.pdf
nnun files list
```

`nnun use <project-id-or-name>` verifies the project and writes a nonsecret `.nnun.json` in your current directory. Exact project names work when unambiguous. Commands find that file in the current directory or an ancestor. Add it to your own `.gitignore` if you do not want to share the project selection. Use `--project <project-id>` to override it.

```sh
nnun projects show
nnun folders create "Drafts"
nnun folders create "Notes" --parent <folder-id>
nnun push paper.tex --folder <folder-id>
nnun files download <file-id>
nnun files download <file-id> --output ./copy.tex
nnun files remove <file-id>                  # asks for the full ID to confirm
nnun files remove <file-id> --yes            # permanent removal
nnun projects list --json
nnun logout
```

Projects default to private. `nnun projects create "Public notes" --visibility public` makes the project and original files readable by anyone with the project link. Downloads never overwrite existing local files. Project/file IDs are UUIDs; folder flags accept folder UUIDs, not paths. The web app also shows IDs in project/file API responses.

## Authentication and API

Login opens the browser and requests explicit approval through Supabase OAuth authorization code + PKCE S256. The local callback binds only `127.0.0.1`, on one of ports 43821–43823. `nnun login --no-browser` prints the URL for a browser on the same computer; login expires after five minutes. State checks prevent unrelated callbacks from completing login.

Access and rotating refresh credentials live in macOS Keychain, Windows Credential Manager, or Linux Secret Service. There is no plaintext credential fallback. Unlock/configure your credential store before login. `nnun logout` removes local credentials; it does not revoke existing access tokens or delete the browser login. Server-side revocation is managed separately through the identity provider.

Headless environments can explicitly supply a short-lived Supabase **user access token** using `NNUN_TOKEN`. Obtain it through your own trusted authenticated flow; do not use a service-role key. Treat the environment variable as a secret. The CLI does not persist, refresh, or erase externally supplied tokens.

`--api-url https://…` or `NNUN_API_URL` selects another Nnun website. Credentials are isolated per exact origin. HTTP is accepted only for loopback development servers. Cross-origin HTTP redirects are refused, and signed storage transfers never contain the app bearer token. Server discovery at `/api/cli/config` provides OAuth and Storage settings. The website must support API version 1 and configure its public OAuth client before interactive login works.

Uploads use immutable reservations → signed direct Storage transfer → server verification. Same-name files in the same folder cause a conflict; a push never overwrites them. Supported text files are UTF-8 `.tex`, `.txt`, `.md` up to 2 MiB, and PDFs up to 6 MiB. Batch pushes validate all files first, then upload sequentially; a later failure does not undo earlier successful files. A failed transfer attempts reservation cleanup. A failure after completion begins preserves the file because the server may have completed it successfully; inspect `nnun files list` before retrying. No background sync, revisions, directory import, or TeX execution is performed.

## Develop and release

Use Go 1.24 or newer:

```sh
go mod download
go test ./...
go test -race ./...
go vet ./...
go build -o nnun .
./nnun --help
```

CI runs on Linux, macOS, and Windows. Tests cover callback state and expiry behavior, credential rotation, origin restrictions, API conflicts, upload cleanup/ambiguous completion, and installer checksums/version pinning. The release workflow builds all six OS/architecture combinations with `CGO_ENABLED=0`, and publishes archives plus `checksums.txt` and installer copies to GitHub Releases using the built-in `GITHUB_TOKEN`. Repository Actions must have permission to create releases.

After merging a tested change to `main`, create and push a semantic version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Tags are immutable release inputs. Every build must succeed before publication. Prerelease tags publish as prereleases and do not become the default latest release. No publishing credential across repositories is required. The private Nnun web app serves identical copies of these installers from `public/install.sh` and `public/install.ps1`; update both copies when installer logic changes. New CLI versions alone do not require website deployment.

Uninstall by removing `nnun`/`nnun.exe` from the installation directory and its PATH entry. Run `nnun logout` first to remove stored CLI credentials.
