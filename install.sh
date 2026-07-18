#!/bin/sh
set -eu

repo=${SUBGEN_REPO:-antonioneris/subgen}
skip_ffmpeg=${SUBGEN_SKIP_FFMPEG:-0}

say() { printf '%s\n' "$*"; }
fail() { say "erro: $*" >&2; exit 1; }
as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        command -v sudo >/dev/null 2>&1 || fail "sudo é necessário para instalar dependências do sistema"
        sudo "$@"
    fi
}

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "sistema não suportado por este instalador; no Windows use install.ps1" ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "arquitetura não suportada: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl não encontrado"
command -v tar >/dev/null 2>&1 || fail "tar não encontrado"

asset="subgen_${os}_${arch}.tar.gz"
base_url="https://github.com/${repo}/releases/latest/download"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/subgen-install.XXXXXX")
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

say "Baixando subgen para ${os}/${arch}..."
curl -fsSL "${base_url}/${asset}" -o "${temp_dir}/${asset}"
curl -fsSL "${base_url}/checksums.txt" -o "${temp_dir}/checksums.txt"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "${temp_dir}/checksums.txt")
[ -n "$expected" ] || fail "checksum de ${asset} não encontrado"
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${temp_dir}/${asset}" | awk '{print $1}')
else
    actual=$(shasum -a 256 "${temp_dir}/${asset}" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || fail "checksum inválido; download recusado"

tar -xzf "${temp_dir}/${asset}" -C "$temp_dir"
[ -x "${temp_dir}/subgen" ] || fail "release não contém o executável subgen"

if [ "$os" = darwin ] && command -v brew >/dev/null 2>&1; then
    bin_dir="$(brew --prefix)/bin"
else
    bin_dir=/usr/local/bin
fi

try_install() {
    local target_dir="$1"
    local dest="${target_dir}/subgen"
    local staged="${dest}.new.$$"

    if [ "$(id -u)" -eq 0 ] || { [ -d "$target_dir" ] && [ -w "$target_dir" ]; }; then
        mkdir -p "$target_dir" && \
        install -m 0755 "${temp_dir}/subgen" "$staged" && \
        mv "$staged" "$dest"
    else
        if command -v sudo >/dev/null 2>&1; then
            sudo mkdir -p "$target_dir" && \
            sudo install -m 0755 "${temp_dir}/subgen" "$staged" && \
            sudo mv "$staged" "$dest"
        else
            return 1
        fi
    fi
}

if try_install "$bin_dir"; then
    destination="${bin_dir}/subgen"
else
    say "Não foi possível gravar em ${bin_dir} (pode ser um sistema de arquivos somente leitura como ZimaOS/SteamOS ou falta de permissão)."
    user_bin_dir="${HOME}/.local/bin"
    if try_install "$user_bin_dir"; then
        destination="${user_bin_dir}/subgen"
        case ":${PATH}:" in
            *:"${user_bin_dir}":*) ;;
            *)
                say ""
                say "⚠ AVISO: ${user_bin_dir} não está no seu PATH."
                say "Para poder executar o subgen de qualquer lugar, adicione a seguinte linha ao seu ~/.bashrc, ~/.zshrc ou perfil do terminal:"
                say "  export PATH=\"\$PATH:${user_bin_dir}\""
                ;;
        esac
    else
        fail "não foi possível instalar o subgen em nenhum dos diretórios disponíveis (/usr/local/bin ou ~/.local/bin)"
    fi
fi

install_ffmpeg() {
    say "FFmpeg não encontrado; instalando a dependência de mídia..."
    if command -v brew >/dev/null 2>&1; then
        brew install ffmpeg
    elif command -v apt-get >/dev/null 2>&1; then
        as_root apt-get update
        as_root apt-get install -y ffmpeg
    elif command -v dnf >/dev/null 2>&1; then
        as_root dnf install -y ffmpeg
    elif command -v yum >/dev/null 2>&1; then
        as_root yum install -y ffmpeg
    elif command -v pacman >/dev/null 2>&1; then
        as_root pacman -Sy --noconfirm ffmpeg
    elif command -v apk >/dev/null 2>&1; then
        as_root apk add ffmpeg
    elif command -v zypper >/dev/null 2>&1; then
        as_root zypper --non-interactive install ffmpeg
    else
        say "aviso: gerenciador de pacotes não reconhecido; instale ffmpeg e ffprobe manualmente" >&2
        return
    fi
}

if [ "$skip_ffmpeg" != 1 ] && { ! command -v ffmpeg >/dev/null 2>&1 || ! command -v ffprobe >/dev/null 2>&1; }; then
    install_ffmpeg
fi

say ""
say "✓ $($destination version) instalado em $destination"
if command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
    say "✓ FFmpeg disponível"
else
    say "⚠ FFmpeg ainda não está no PATH; ele é necessário para arquivos de vídeo"
fi
say "Execute: subgen config"
