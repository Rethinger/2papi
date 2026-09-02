#!/bin/sh
# 2papi universal installer — Linux/macOS
# Usage: curl -fsSL https://raw.githubusercontent.com/Rethinger/2papi/main/install.sh | sh
set -e

REPO="Rethinger/2papi"
BIN="2papi"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-latest}"

detect_os_arch() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)
  case "$OS" in
    linux) OS="linux" ;;
    darwin) OS="darwin" ;;
    *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
  esac
  case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported arch: $ARCH" >&2; exit 1 ;;
  esac
  echo "${OS}_${ARCH}"
}

get_latest_version() {
  if [ "$VERSION" != "latest" ]; then
    echo "$VERSION"
    return
  fi
  LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/' || true)
  if [ -z "$LATEST" ]; then
    # No published release (or the API is unreachable). Don't guess a tag —
    # a made-up version just turns this into a confusing 404 later.
    echo "No published release found for ${REPO}." >&2
    echo "Install from source instead:" >&2
    echo "  go install github.com/Rethinger/2papi/cmd/gateway@master" >&2
    echo "or run the full stack with Docker:" >&2
    echo "  docker compose up --build" >&2
    echo "To pin a specific tag once releases exist: VERSION=vX.Y.Z $0" >&2
    exit 1
  fi
  echo "$LATEST"
}

main() {
  PLATFORM=$(detect_os_arch)
  TAG=$(get_latest_version)
  echo "→ Installing 2papi ${TAG} for ${PLATFORM} ..."

  # Lite mode: download binary from GitHub Releases
  # Fallback: build from source if release not found
  TMPDIR=$(mktemp -d)
  trap 'rm -rf "$TMPDIR"' EXIT

  URL="https://github.com/${REPO}/releases/download/${TAG}/2papi_${PLATFORM}.tar.gz"
  echo "→ Downloading ${URL}"
  if ! curl -fsSL "$URL" -o "$TMPDIR/2papi.tar.gz"; then
    echo "→ Release not found, building from source (requires Go)..."
    if ! command -v go >/dev/null 2>&1; then
      echo "Error: Go not found and no prebuilt binary for ${TAG}. Install Go or use Docker: docker compose up" >&2
      exit 1
    fi
    git clone --depth 1 "https://github.com/${REPO}.git" "$TMPDIR/src" 2>/dev/null || cp -r . "$TMPDIR/src"
    (cd "$TMPDIR/src" && go build -ldflags="-s -w" -o "$TMPDIR/2papi" ./cmd/gateway)
    BINARY="$TMPDIR/2papi"
  else
    tar -xzf "$TMPDIR/2papi.tar.gz" -C "$TMPDIR"
    BINARY="$TMPDIR/2papi"
    chmod +x "$BINARY"
  fi

  # Install
  if [ -w "$INSTALL_DIR" ]; then
    cp "$BINARY" "$INSTALL_DIR/$BIN"
  else
    echo "→ Need sudo to copy to $INSTALL_DIR"
    sudo cp "$BINARY" "$INSTALL_DIR/$BIN"
  fi

  echo "✓ Installed to $INSTALL_DIR/$BIN"

  # Init config if missing
  CONFIG_DIR="${HOME}/.2papi"
  if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
    mkdir -p "$CONFIG_DIR"
    if [ -f "./config/example.yaml" ]; then
      cp ./config/example.yaml "$CONFIG_DIR/config.yaml"
    else
      cat > "$CONFIG_DIR/config.yaml" <<'YAML'
version: 1
secret: change-me-generate-random
server:
  addr: ":8080"
virtual_keys:
  - name: dev
    key: sk-gateway-dev
    models: [gpt-dev]
    rpm: 60
models:
  - alias: gpt-dev
    upstream_model: gpt-4o-mini
    accounts: [primary]
accounts:
  - name: primary
    base_url: http://localhost:9001
    api_key: sk-test
    enabled: true
YAML
    fi
    echo "✓ Config created at $CONFIG_DIR/config.yaml — edit and run: 2papi --config $CONFIG_DIR/config.yaml"
  fi

  echo ""
  echo "Run:"
  echo "  2papi tui                    # interactive menu (Start/Providers/Quota/Plugins)"
  echo "  2papi init                   # enable 2papi.local via hosts"
  echo "  2papi --config $CONFIG_DIR/config.yaml"
  echo "  or (Docker): docker compose up --build"
  echo "  Dashboard: http://localhost:8080/dashboard/"
  echo "  Gateway:   http://localhost:8080/v1/chat/completions"
}

main "$@"

