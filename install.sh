#!/bin/sh
set -eu

die() {
    printf 'error: %s\n' "$*" >&2
    exit 1
}

main() {
    platform=$(uname -s)/$(uname -m)
    case "$platform" in
        Linux/x86_64|Linux/amd64) target=linux-amd64 ;;
        Linux/aarch64|Linux/arm64) target=linux-arm64 ;;
        Darwin/arm64) target=darwin-arm64 ;;
        *) die "unsupported platform: $platform; download a supported ZIP from https://github.com/ataca-io/wagon/releases/latest" ;;
    esac

    for tool in curl unzip awk mktemp; do
        command -v "$tool" >/dev/null 2>&1 || die "required command not found: $tool"
    done
    if command -v sha256sum >/dev/null 2>&1; then
        checksum_tool=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        checksum_tool=shasum
    else
        die "SHA-256 verification requires sha256sum or shasum"
    fi
    [ -n "${HOME:-}" ] || die "HOME is not set"

    repository=https://github.com/ataca-io/wagon
    release_url=$(curl -fsSLI --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "$repository/releases/latest") || die "could not resolve the latest release"
    case "$release_url" in
        "$repository/releases/tag/"v[0-9]*) version=${release_url##*/} ;;
        *) die "unexpected latest release URL: $release_url" ;;
    esac
    archive=wagon-$target.zip
    download_url=$repository/releases/download/$version
    tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wagon-install.XXXXXX") || die "could not create a temporary directory"
    staged=
    trap 'rm -rf "$tmp_dir"; if [ -n "$staged" ]; then rm -f "$staged"; fi' 0
    trap 'exit 1' HUP INT TERM

    curl -fsSL --proto '=https' --proto-redir '=https' -o "$tmp_dir/$archive" "$download_url/$archive" || die "could not download $archive"
    curl -fsSL --proto '=https' --proto-redir '=https' -o "$tmp_dir/SHA256SUMS" "$download_url/SHA256SUMS" || die "could not download SHA256SUMS"
    awk -v name="$archive" '$2 == name { print; found=1 } END { if (!found) exit 1 }' "$tmp_dir/SHA256SUMS" > "$tmp_dir/checksum" || die "checksum missing for $archive"
    if [ "$checksum_tool" = sha256sum ]; then
        (cd "$tmp_dir" && sha256sum -c checksum) || die "checksum verification failed for $archive"
    else
        (cd "$tmp_dir" && shasum -a 256 -c checksum) || die "checksum verification failed for $archive"
    fi
    unzip -p "$tmp_dir/$archive" wagon > "$tmp_dir/wagon" || die "could not extract wagon from $archive"

    install_dir=$HOME/.local/bin
    destination=$install_dir/wagon
    mkdir -p "$install_dir" || die "could not create $install_dir"
    [ ! -d "$destination" ] || die "$destination is a directory"
    staged=$(mktemp "$install_dir/.wagon.XXXXXX") || die "could not stage the installation"
    cat "$tmp_dir/wagon" > "$staged" || die "could not write $staged"
    chmod 755 "$staged" || die "could not make wagon executable"
    mv -f "$staged" "$destination" || die "could not install wagon to $destination"
    staged=
    printf 'Installed wagon %s to %s\n' "$version" "$destination"
    if [ "$(command -v wagon || true)" != "$destination" ]; then
        # shellcheck disable=SC2016 # Print variables for the user's shell.
        printf 'To use this installation, run: export PATH="$HOME/.local/bin:$PATH"\n'
    fi
}

main "$@"
