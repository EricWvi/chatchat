#!/bin/sh
# Fails unless the Go build would embed a complete frontend build:
# index.html, the icon and every asset index.html references. Run after
# the frontend build and before go build.
set -eu
cd "$(dirname "$0")/.."

embedded=$(go list -f '{{join .EmbedFiles "\n"}}' .)
missing=""
need() {
  printf '%s\n' "$embedded" | grep -qxF "frontend/dist/$1" || missing="$missing $1"
}

for f in index.html favicon.svg; do need "$f"; done
assets=$(grep -oE '(src|href)="/assets/[^"]+"' frontend/dist/index.html 2>/dev/null |
  sed -E 's/^(src|href)="\///; s/"$//' || true)
[ -n "$assets" ] || missing="$missing assets/*(none referenced by index.html)"
for f in $assets; do need "$f"; done
printf '%s\n' "$assets" | grep -q '\.js$' || missing="$missing assets/*.js"
printf '%s\n' "$assets" | grep -q '\.css$' || missing="$missing assets/*.css"

if [ -n "$missing" ]; then
  echo "frontend not embedded, missing:$missing" >&2
  echo "build the frontend first (cd frontend && npm run build)" >&2
  exit 1
fi
echo "embedded frontend: $(printf '%s\n' "$embedded" | grep -vc '/\.gitkeep$') files"
