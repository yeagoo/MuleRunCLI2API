#!/usr/bin/env bash
# get-muk-key.sh — print your MuleRun studio API key (muk-...).
#
# cli2api needs a stable per-account `muk-` API key in $MULERUN_TOKEN. The
# mulerun CLI doesn't expose it directly, but it does set MULEROUTER_API_KEY in
# the env when it invokes the bundled mulerouter binary. This script wraps that
# binary for one call, captures the value, and restores it.
#
# Usage:
#   bash get-muk-key.sh                 # prints muk-...
#   export MULERUN_TOKEN=$(bash get-muk-key.sh)
#
# Or one-shot from GitHub:
#   curl -fsSL https://raw.githubusercontent.com/yeagoo/MuleRunCLI2API/master/scripts/get-muk-key.sh | bash

set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }

command -v mulerun >/dev/null 2>&1 || die "mulerun CLI not installed. Install it: npm i -g @mulerunai/cli"

OAUTH_CACHE="${HOME}/.config/mulerun/oauth_cache.json"
[[ -f "$OAUTH_CACHE" ]] || die "not logged in. Run: mulerun login"

WRAPPER_LINK="${HOME}/.mulerun/vendor/mulerouter/node_modules/.bin/mulerouter"
[[ -L "$WRAPPER_LINK" || -e "$WRAPPER_LINK" ]] || die "mulerouter binary not found at $WRAPPER_LINK. Try: mulerun studio list (this seeds the vendor dir)"

WRAPPER="$(readlink -f "$WRAPPER_LINK")"
BACKUP="${WRAPPER}.real"

[[ -e "$BACKUP" ]] && die "$BACKUP already exists — a prior run was interrupted. Inspect and \`mv \"$BACKUP\" \"$WRAPPER\"\` to restore, then retry."

cleanup() { [[ -e "$BACKUP" ]] && mv -f "$BACKUP" "$WRAPPER"; }
trap cleanup EXIT INT TERM

mv "$WRAPPER" "$BACKUP"
cat > "$WRAPPER" <<'STUB'
#!/usr/bin/env bash
# stub installed by cli2api/scripts/get-muk-key.sh — prints MULEROUTER_API_KEY
printf '%s' "${MULEROUTER_API_KEY:-}"
STUB
chmod +x "$WRAPPER"

KEY="$(mulerun studio list 2>/dev/null || true)"
KEY="${KEY##*$'\n'}"   # take last line in case mulerun prints anything before

[[ -n "$KEY" ]] || die "captured empty key. Try: mulerun user balance (to confirm login is valid), then retry."
[[ "$KEY" == muk-* ]] || die "captured value does not look like a muk- key: ${KEY:0:8}..."

printf '%s\n' "$KEY"
