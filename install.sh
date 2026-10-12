#!/bin/sh
# Nnun installer. Downloads only one resolved release and verifies SHA-256.
set -eu

fail() { printf '%s\n' "nnun: $*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail 'curl is required.'
command -v tar >/dev/null 2>&1 || fail 'tar is required.'

version=${NNUN_VERSION:-}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || fail '--version needs a value.'; version=$2; shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
case "$(uname -s)" in Darwin) platform=darwin ;; Linux) platform=linux ;; *) fail 'Supported platforms: macOS and Linux. Use install.ps1 on Windows.' ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) fail 'Supported architectures: x64 and ARM64.' ;; esac
release_root=https://github.com/Savokr/nnun-cli/releases
if [ -z "$version" ]; then
  resolved=$(curl -fsSL --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "$release_root/latest") || fail 'Could not find the latest release.'
  version=${resolved##*/}
fi
version=${version#v}
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$' || fail 'Invalid version; expected 0.1.0 or v0.1.0.'

install_dir=${NNUN_INSTALL_DIR:-"$HOME/.local/bin"}
case "$install_dir" in /*) ;; *) fail 'NNUN_INSTALL_DIR must be an absolute path.' ;; esac
case "$install_dir" in *'
'*|*"$(printf '\r')"*) fail 'Install path must not contain line breaks.' ;; esac
mkdir -p "$install_dir"
temp_dir=$(mktemp -d "$install_dir/.nnun-install.XXXXXXXX") || fail 'Could not create temporary directory.'
trap 'rm -f "$temp_dir/archive.tar.gz" "$temp_dir/checksums.txt" "$temp_dir/nnun"; rmdir "$temp_dir" 2>/dev/null || true' EXIT HUP INT TERM
asset="nnun_${version}_${platform}_${arch}.tar.gz"
base="$release_root/download/v$version"
printf 'Installing nnun %s for %s/%s…\n' "$version" "$platform" "$arch"
curl -fsSL --retry 3 --proto '=https' --proto-redir '=https' "$base/$asset" -o "$temp_dir/archive.tar.gz" || fail 'Release download failed.'
curl -fsSL --retry 3 --proto '=https' --proto-redir '=https' "$base/checksums.txt" -o "$temp_dir/checksums.txt" || fail 'Checksum download failed.'
expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$temp_dir/checksums.txt")
printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[a-fA-F0-9]{64}$' || fail 'Missing or invalid release checksum.'
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temp_dir/archive.tar.gz" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temp_dir/archive.tar.gz" | awk '{print $1}')
else
  fail 'sha256sum or shasum is required.'
fi
[ "$actual" = "$expected" ] || fail 'Checksum mismatch; installation stopped.'
tar -xzf "$temp_dir/archive.tar.gz" -C "$temp_dir" nnun || fail 'Invalid release archive.'
[ -f "$temp_dir/nnun" ] && [ ! -L "$temp_dir/nnun" ] || fail 'Archive did not contain a regular nnun binary.'
chmod 755 "$temp_dir/nnun"
mv -f "$temp_dir/nnun" "$install_dir/nnun"

# Quote apostrophes before writing shell code; never evaluate downloaded content.
quoted_dir=$(printf '%s' "$install_dir" | sed "s/'/'\\\\''/g")
path_line="export PATH='$quoted_dir':\"\$PATH\""
case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *)
    login_shell=${SHELL:-}
    case "${login_shell##*/}" in
      zsh) profile="$HOME/.zshrc" ;;
      bash) profile="$HOME/.bashrc" ;;
      fish) profile= ;;
      *) profile="$HOME/.profile" ;;
    esac
    if [ -n "$profile" ]; then
      if [ ! -f "$profile" ] || ! grep -Fqx "$path_line" "$profile"; then
        printf '\n# Nnun CLI\n%s\n' "$path_line" >> "$profile"
      fi
      printf 'PATH configured in %s. Open a new terminal to use nnun.\n' "$profile"
    else
      printf 'Add %s to your fish PATH, then open a new terminal.\n' "$install_dir"
    fi
    ;;
esac
"$install_dir/nnun" --version
printf 'Next: nnun login\n'
