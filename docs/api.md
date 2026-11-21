# The addon's API

Two surfaces, both on the daemon's loopback port (8300):

- **The page's API** under `/addons/notify/api/v1/`, reached through the system's web server. Every request carries
  the shell's session (administrators only), every write the header `X-CCU-Addon-Request: 1`. JSON in and out; an error
  is `{"error", "message"}`, a validation error `422 {"error": "invalid", "field", "message"}`.
- **The inbound webhook** `POST /inbound`, which the system's web server does not proxy: only a program on the system
  itself reaches it, at `http://127.0.0.1:8300/inbound`, with the secret.

Secrets are never answered: a channel's `token`, `user_key`, `password`, its header values and, for Discord, Slack and
Matrix, its `url` come back empty, with `secrets: {"token": true, …}` saying which are set. A save with an empty secret
keeps the stored one; `clear: ["password"]` removes one.

## The page's API

| Route | |
| --- | --- |
| `GET health` | `{ok, version, uptime_s}` |
| `GET config` | The configuration without its secrets: `{settings, channels, rules, defaults: {settings, rules}, version, system, types, warning_ids, filters}` — `types` the channel types, `warning_ids` the Status-page warnings with a title of their own, `filters` the template helpers. Every route that changes the configuration answers the same |
| `POST channels` | Adds a channel: `{name, type, enabled, …}` with the fields of its type (below). The id is made from the name |
| `PUT channels/{id}` | Replaces a channel; secrets left empty are kept |
| `DELETE channels/{id}` | Removes a channel and takes it out of every rule; a rule that named only this channel is switched off |
| `POST channels/test` | Sends a test message with the given settings, saved or not (with an `id`, empty secrets are the stored ones). `200 {ok}`, or `502 {error: "send", step, message}`: `step` is `config`, `template`, `resolve`, `connect`, `tls`, `login`, `sender`, `recipient`, `send` or `answer`, `message` what the target or the network said |
| `POST rules` | Adds a rule (below) |
| `PUT rules/{id}` | Replaces a rule |
| `DELETE rules/{id}` | Removes a rule |
| `POST rules/preview` | A rule as the form holds it → the message it would send, made of sample events: `{title, body, priority}`; `422` names a broken template |
| `POST rules/{id}/test` | Sends the rule's sample message to its channels, through their queues: `{ok, channels, title}`; `409 no-channel` when it has no enabled channel |
| `PUT settings` | `{language, system_name, base_url, title_prefix, quiet: {enabled, from, to}, repeat_min, rate_per_hour, inbound: {enabled}}` |
| `GET inbound` | The inbound webhook: `{enabled, url, secret, per_minute}` |
| `POST inbound/rotate` | A new secret; the old one stops working at once. Answers as `GET inbound` |
| `GET status` | `{now, sources: {warnings, service, system, datapoints: {ok, at, error?, open, watching, stream}}, open: {warnings, service, datapoints}, channels: {<id>: {queued, sent, failed, last_ok?, last_error?, last_step?, last_error_at?, failing, held}}, pending, quiet_until?}` |
| `GET history` | The last 200 deliveries, newest first: `[{time, message, channel, channel_name, channel_type, rule?, rule_name?, kind, title, body?, result, attempt?, step?, error?}]`; `result` is `sent`, `failed` (another attempt follows), `gave-up`, `limited` (the rate limit) or `dropped` |
| `DELETE history` | Empties the history |
| `GET devices` | The devices and channels with the datapoints the system holds values of, for the datapoint rule's picker: `[{interface, address, name, rooms, keys: [{key, value}]}]` |

### A channel's fields

| Type | Fields |
| --- | --- |
| `pushover` | `token` (the application's token), `user_key`, `target` (a device, optional), `sound` (optional) |
| `telegram` | `token` (the bot's), `target` (the chat id), `thread` (a topic id, optional) |
| `email` | `host`, `port`, `security` (`starttls`, `tls`, `none`), `user`, `password`, `sender`, `target` (the recipients, comma-separated), `insecure` |
| `ntfy` | `url` (the server; `https://ntfy.sh` when empty), `target` (the topic), `token` or `user` and `password`, `insecure` |
| `gotify` | `url`, `token` (the application's), `insecure` |
| `signal` | `url` (a signal-cli REST server), `sender` (its number), `target` (the recipients, comma-separated), `user`, `password`, `insecure` |
| `discord`, `slack`, `matrix` | `url` (the webhook's; for Matrix a webhook bridge that takes `{text, html}`) |
| `webhook` | `url`, `method` (`POST`, `PUT`), `headers`, `content_type`, `body` (a template; empty sends the message as JSON), `insecure` |
| `mqtt` | `host` (`127.0.0.1` when empty), `port`, `security` (`none`, `tls`), `user`, `password`, `target` (the topic, a template; `notify/{{ kind }}` when empty), `body` (the payload, a template; empty sends the message as JSON), `qos` (0, 1), `retain`, `insecure` |

`insecure` accepts any server certificate. Pushover's and Telegram's `url` replaces their API address (a proxy).

### A rule

`{id, name, enabled, source, channels, priority, resolved, batch_s, ignore_quiet, title, body}` and the source's filter:

| `source` | Filter |
| --- | --- |
| `warning` | `severities` (`error`, `warning`, `info`), `exclude_ids` |
| `service` | `kinds` (`unreach`, `fault`, `lowbat`, `other`, `config`, `update`, `sticky`), `exclude` (device addresses), `hold_s` |
| `datapoint` | `datapoint: {interface, address, key, op, value, for_s, label}`; `op` is `true`, `false`, `eq`, `ne`, `gt`, `ge`, `lt`, `le` |
| `system` | `kinds` (`update-available`, `update-installed`, `backup-done`, `backup-failed`, `reboot`) |
| `inbound` | `tag` (the sender's `source`; empty matches all) |
| `digest` | `time` (`HH:MM`), `only_open` |

`channels` empty means every enabled channel. `priority` is `low`, `normal` or `high`. `title` and `body` are templates
that replace the defaults.

### The message

What a webhook without a body template and an MQTT channel without a payload template send, and what a template sees:

```json
{"id": "9f3c…", "kind": "service", "rule": "service", "rule_name": "Servicemeldungen der Geräte",
 "title": "Heizung Bad: Kommunikation gestört", "body": "Bad · HmIP-RF 000A1BE9A1B2C3 · seit 21.11. 14:03",
 "priority": "normal", "resolved": false, "link": "https://ccu.example/", "system": "ccu",
 "time": "2026-11-21T14:05:03+01:00", "count": 1,
 "items": [{"key": "HmIP-RF|000A1BE9A1B2C3|UNREACH", "title": "Heizung Bad", "text": "Kommunikation gestört",
            "line": "Heizung Bad: Kommunikation gestört", "detail": "Bad · HmIP-RF 000A1BE9A1B2C3 · seit 21.11. 14:03",
            "resolved": false, "device": "Heizung Bad", "interface": "HmIP-RF", "address": "000A1BE9A1B2C3",
            "rooms": ["Bad"], "datapoint": "UNREACH", "value": true, "since": "2026-11-21T14:03:02+01:00"}]}
```

A template's variables: `system`, `kind`, `rule` (the rule's name), `resolved`, `count`, `headline` (the default title),
`title`, `body`, `lines` (one ready-made line per item), `items` (as above), `priority`, `link`, `time`.

## The inbound webhook

`POST /inbound` on `http://127.0.0.1:8300`. Off until switched on in the settings.

- **The secret**: `Authorization: Bearer <secret>`, or the header `X-Notify-Secret`, or `?secret=`.
- **A JSON body**: `{title, text, priority, link, source, key, resolved}` — `message` or `body` are read as `text`,
  `url` as `link`, `kind` or `tag` as `source`; `priority` is `low`, `normal`, `high` or a number (4 and above is high).
  `source` is what an inbound rule's tag matches; it may also be given as `?source=`, which wins. With a `key`, a later
  message with the same `source` and `key` and `resolved: true` closes the first.
- **Plain text**: the body is the text, `?title=` the title; without a title the first line is it.
- `202 {"ok": true}`; `401 secret`, `404 disabled`, `400 invalid`, `413 too-large` (1 MiB), `429 rate` (60 a minute).

```sh
curl -s -H "Authorization: Bearer $SECRET" -H 'Content-Type: application/json' \
  -d '{"title":"Waschmaschine fertig","text":"Programm beendet um 14:03","source":"haus"}' \
  http://127.0.0.1:8300/inbound
```
