# Changelog

## [0.1.0] - 2026-11-21

The first release, for openccu-lite 1.2.0 and later.

- **What it reports:** the Status page's warnings, new and resolved (the notices other addons post there included);
  the devices' service messages - communication disturbed, battery low, sabotage and faults, configuration or
  firmware update pending - new and resolved; datapoint rules of your own (a value is true or false, equals, is above
  or below a threshold, for longer than a time you set); system events (system update available, update installed,
  backup made, backup failed, restart); a daily digest that says "all clear" or what is open.
- **Where it sends:** Pushover, Telegram (a bot), email (SMTP with STARTTLS, TLS or neither, with or without a login),
  ntfy, Gotify, Signal (through a signal-cli REST server you run), Discord, Slack and Matrix (through their webhook
  URLs; Matrix through a webhook bridge), a generic webhook (the message as JSON, or a body template) and MQTT (the
  local Mosquitto by default). Every channel has a Test button that names the step that failed - the name does not
  resolve, the connection is refused, the certificate is not trusted, the login is refused, the sender or a recipient
  is rejected - with the server's own answer.
- **Rules:** a rule is a source, a filter and the channels it sends to; three are there from the start and send to
  every channel, so the first channel you add receives warnings, service messages and system events at once. Batching
  (several events within the window are one message; one that comes and goes within it is none), a hold time for
  service messages, quiet hours (what happens meanwhile arrives as one message when they end; a rule can ignore
  them), repeat suppression, a rate limit per channel, three priorities mapped to each channel's own, "resolved"
  messages, and title and body templates (Jinja syntax) with a live preview.
- **Delivery:** a queue per channel, retries after 10 s, 1 min, 5 min and 15 min, a history of the last 200
  deliveries with the reason of every failure; a channel that keeps failing shows as a notice on the Status page.
- **The inbound webhook:** `POST http://127.0.0.1:8300/inbound` with a secret, for other addons, Node-RED flows and
  scripts on the system; off until switched on, reachable from the system itself only.
- **The page:** Status, Channels, Rules, History, Settings; German and English, light and dark.
- Reads from the system only (`system:read`, `rpc:read`, `meta:read`); opens no port to the network. "Backup made"
  needs openccu-lite 2.0.0 or later.
