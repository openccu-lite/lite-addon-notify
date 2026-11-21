#!/bin/sh
#
#   Runs the daemon against the fake system, for the page's development and the Playwright
#   tests: the fake system on 127.0.0.1:18080 (its /sink/… records what the channels send, its
#   SMTP server on 127.0.0.1:12525 takes mail from the user "user" with the password "pass"), the
#   page on http://127.0.0.1:18300/addons/notify/ (no session check), the inbound webhook on
#   http://127.0.0.1:18300/inbound. A fresh configuration directory each run unless
#   NOTIFY_CONF_DIR names one. The page is served from ui/dist (npm run build first).
#
set -e
cd "$(dirname "$0")/.."
DIR=${NOTIFY_CONF_DIR:-$(mktemp -d)}
mkdir -p build "$DIR/addon"
go build -o build/fakesystem ./cmd/fakesystem
go build -o build/notify-dev ./cmd/notify
echo dev > "$DIR/token"
build/fakesystem -addr 127.0.0.1:18080 -smtp 127.0.0.1:12525 &
FAKE=$!
sleep 0.5
build/notify-dev -system http://127.0.0.1:18080 -no-session-check -port 18300 -conf-dir "$DIR" -addon-dir "$DIR/addon" -www ui/dist -token-file "$DIR/token" &
DAEMON=$!
trap 'kill $DAEMON $FAKE 2>/dev/null' EXIT INT TERM
wait $DAEMON
