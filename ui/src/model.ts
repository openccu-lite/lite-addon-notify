// The API's shapes (docs/api.md) and what the page derives from them.

export type ChannelType = 'pushover' | 'telegram' | 'email' | 'ntfy' | 'gotify' | 'signal' | 'discord' | 'slack' | 'matrix' | 'webhook' | 'mqtt';
export type Source = 'warning' | 'service' | 'datapoint' | 'system' | 'inbound' | 'digest';
export type Priority = 'low' | 'normal' | 'high';

export interface Channel {
    id: string;
    name: string;
    type: ChannelType;
    enabled: boolean;
    url?: string;
    token?: string;
    user_key?: string;
    user?: string;
    password?: string;
    target?: string;
    sender?: string;
    host?: string;
    port?: number;
    security?: string;
    insecure?: boolean;
    method?: string;
    headers?: Record<string, string>;
    content_type?: string;
    body?: string;
    thread?: string;
    sound?: string;
    qos?: number;
    retain?: boolean;
    /** which secrets are stored (the API never answers them) */
    secrets?: Record<string, boolean>;
    clear?: string[];
}

export interface Datapoint {
    interface: string;
    address: string;
    key: string;
    op: string;
    value?: string;
    for_s?: number;
    label?: string;
}

export interface Rule {
    id: string;
    name: string;
    enabled: boolean;
    source: Source;
    channels: string[];
    priority: Priority;
    resolved: boolean;
    batch_s: number;
    ignore_quiet: boolean;
    severities?: string[];
    exclude_ids?: string[];
    kinds?: string[];
    exclude?: string[];
    hold_s?: number;
    datapoint?: Datapoint;
    tag?: string;
    time?: string;
    only_open?: boolean;
    title?: string;
    body?: string;
}

export interface Settings {
    language: string;
    system_name: string;
    base_url: string;
    title_prefix: boolean;
    quiet: {enabled: boolean; from: string; to: string};
    repeat_min: number;
    rate_per_hour: number;
    inbound: {enabled: boolean};
}

export interface Config {
    settings: Settings;
    channels: Channel[];
    rules: Rule[];
    defaults: {settings: Settings; rules: Rule[]};
    version: string;
    system: string;
    types: ChannelType[];
    warning_ids: string[];
    filters: {name: string; doc: Record<string, string>}[];
}

export interface Item {
    key: string;
    title: string;
    text?: string;
    line: string;
    severity?: string;
}

export interface SourceState {
    ok: boolean;
    at?: string;
    error?: string;
    open: number;
    watching: number;
    stream: boolean;
}

export interface ChannelStatus {
    queued: number;
    sent: number;
    failed: number;
    last_ok?: string;
    last_error?: string;
    last_step?: string;
    last_error_at?: string;
    failing: boolean;
    held: number;
}

export interface Status {
    now: number;
    sources: Record<string, SourceState>;
    open: {warnings: Item[]; service: Item[]; datapoints: Item[]};
    channels: Record<string, ChannelStatus>;
    pending: number;
    quiet_until?: number;
}

export interface HistoryRow {
    time: string;
    message: string;
    channel: string;
    channel_name: string;
    channel_type: string;
    rule?: string;
    rule_name?: string;
    kind: string;
    title: string;
    body?: string;
    result: string;
    attempt?: number;
    step?: string;
    error?: string;
}

export interface Device {
    interface: string;
    address: string;
    name: string;
    rooms: string[];
    keys: {key: string; value: unknown}[];
}

export interface InboundInfo {
    enabled: boolean;
    url: string;
    secret: string;
    per_minute: number;
}

export type FieldKind = 'text' | 'secret' | 'number' | 'select' | 'check' | 'area' | 'headers';

/** one field of a channel's form: which property, how it is edited, its label and help (keys of the catalogue) */
export interface Field {
    name: keyof Channel;
    kind: FieldKind;
    label: string;
    help?: string;
    placeholder?: string;
    options?: {value: string; label: string}[];
    required?: boolean;
    /** in the dialog's "more" part */
    advanced?: boolean;
}

const INSECURE: Field = {name: 'insecure', kind: 'check', label: 'Accept any certificate (a self-signed server)', advanced: true};

/** The forms, per channel type, in the order the dialog shows them. */
export const FIELDS: Record<ChannelType, Field[]> = {
    pushover: [
        {name: 'token', kind: 'secret', label: 'Application token', help: 'help.pushover', required: true},
        {name: 'user_key', kind: 'secret', label: 'User or group key', required: true},
        {name: 'target', kind: 'text', label: 'Device (optional)', advanced: true},
        {name: 'sound', kind: 'text', label: 'Sound (optional)', advanced: true},
    ],
    telegram: [
        {name: 'token', kind: 'secret', label: 'Bot token', help: 'help.telegram', required: true},
        {name: 'target', kind: 'text', label: 'Chat id', required: true, placeholder: '-1001234567890'},
        {name: 'thread', kind: 'text', label: 'Topic id (optional)', advanced: true},
    ],
    email: [
        {name: 'host', kind: 'text', label: 'Mail server (SMTP)', required: true, placeholder: 'smtp.example.org'},
        {
            name: 'security',
            kind: 'select',
            label: 'Encryption',
            help: 'help.email-security',
            options: [
                {value: 'starttls', label: 'STARTTLS (port 587)'},
                {value: 'tls', label: 'TLS (port 465)'},
                {value: 'none', label: 'None (port 25)'},
            ],
        },
        {name: 'port', kind: 'number', label: 'Port'},
        {name: 'user', kind: 'text', label: 'User name'},
        {name: 'password', kind: 'secret', label: 'Password', help: 'help.email-password'},
        {name: 'sender', kind: 'text', label: 'Sender', required: true, placeholder: 'ccu@example.org'},
        {name: 'target', kind: 'text', label: 'Recipients', help: 'help.recipients', required: true},
        INSECURE,
    ],
    ntfy: [
        {name: 'url', kind: 'text', label: 'Server', placeholder: 'https://ntfy.sh', help: 'help.ntfy'},
        {name: 'target', kind: 'text', label: 'Topic', required: true},
        {name: 'token', kind: 'secret', label: 'Access token (optional)'},
        {name: 'user', kind: 'text', label: 'User name', advanced: true},
        {name: 'password', kind: 'secret', label: 'Password', advanced: true},
        INSECURE,
    ],
    gotify: [
        {name: 'url', kind: 'text', label: 'Server', required: true, placeholder: 'https://gotify.example.org'},
        {name: 'token', kind: 'secret', label: 'Application token', required: true},
        INSECURE,
    ],
    signal: [
        {name: 'url', kind: 'text', label: 'Signal REST server', help: 'help.signal', required: true, placeholder: 'http://signal.example.org:8080'},
        {name: 'sender', kind: 'text', label: 'Sender number', required: true, placeholder: '+491701234567'},
        {name: 'target', kind: 'text', label: 'Recipients', help: 'help.signal-recipients', required: true},
        {name: 'user', kind: 'text', label: 'User name', advanced: true},
        {name: 'password', kind: 'secret', label: 'Password', advanced: true},
        INSECURE,
    ],
    discord: [{name: 'url', kind: 'secret', label: 'Webhook URL', help: 'help.discord', required: true}],
    slack: [{name: 'url', kind: 'secret', label: 'Webhook URL', help: 'help.slack', required: true}],
    matrix: [{name: 'url', kind: 'secret', label: 'Webhook URL', help: 'help.matrix', required: true}],
    webhook: [
        {name: 'url', kind: 'text', label: 'URL', required: true, placeholder: 'https://example.org/hook'},
        {
            name: 'method',
            kind: 'select',
            label: 'Method',
            options: [
                {value: 'POST', label: 'POST'},
                {value: 'PUT', label: 'PUT'},
            ],
        },
        {name: 'headers', kind: 'headers', label: 'Headers', help: 'help.headers'},
        {name: 'body', kind: 'area', label: 'Body template (optional)', help: 'help.webhook-body'},
        {name: 'content_type', kind: 'text', label: 'Content type', advanced: true, placeholder: 'application/json'},
        INSECURE,
    ],
    mqtt: [
        {name: 'host', kind: 'text', label: 'Broker', placeholder: '127.0.0.1', help: 'help.mqtt'},
        {
            name: 'security',
            kind: 'select',
            label: 'Encryption',
            options: [
                {value: 'none', label: 'None (port 1883)'},
                {value: 'tls', label: 'TLS (port 8883)'},
            ],
        },
        {name: 'port', kind: 'number', label: 'Port'},
        {name: 'user', kind: 'text', label: 'User name'},
        {name: 'password', kind: 'secret', label: 'Password'},
        {name: 'target', kind: 'text', label: 'Topic', placeholder: 'notify/{{ kind }}', help: 'help.mqtt-topic'},
        {name: 'body', kind: 'area', label: 'Payload template (optional)', help: 'help.mqtt-payload', advanced: true},
        {
            name: 'qos',
            kind: 'select',
            label: 'QoS',
            advanced: true,
            options: [
                {value: '0', label: '0'},
                {value: '1', label: '1'},
            ],
        },
        {name: 'retain', kind: 'check', label: 'Retain the last message', advanced: true},
        INSECURE,
    ],
};

export const TYPE_NAMES: Record<ChannelType, string> = {
    pushover: 'Pushover',
    telegram: 'Telegram',
    email: 'E-Mail',
    ntfy: 'ntfy',
    gotify: 'Gotify',
    signal: 'Signal',
    discord: 'Discord',
    slack: 'Slack',
    matrix: 'Matrix',
    webhook: 'Webhook',
    mqtt: 'MQTT',
};

export const SOURCES: Source[] = ['warning', 'service', 'datapoint', 'system', 'inbound', 'digest'];
export const SEVERITIES = ['error', 'warning', 'info'];
export const SERVICE_KINDS = ['unreach', 'fault', 'lowbat', 'other', 'config', 'update', 'sticky'];
export const SYSTEM_KINDS = ['update-available', 'update-installed', 'backup-done', 'backup-failed', 'reboot'];
export const OPS = ['true', 'false', 'eq', 'ne', 'gt', 'ge', 'lt', 'le'];
export const PRIORITIES: Priority[] = ['low', 'normal', 'high'];
export const RESULTS = ['sent', 'failed', 'gave-up', 'limited', 'dropped'];
export const STEPS = ['config', 'template', 'resolve', 'connect', 'tls', 'login', 'sender', 'recipient', 'send', 'answer'];

export function emptyChannel(type: ChannelType): Channel {
    const ch: Channel = {id: '', name: '', type, enabled: true};
    if (type === 'email') ch.security = 'starttls';
    if (type === 'mqtt') ch.security = 'none';
    if (type === 'webhook') ch.method = 'POST';
    return ch;
}

export function emptyRule(source: Source): Rule {
    const r: Rule = {id: '', name: '', enabled: true, source, channels: [], priority: 'normal', resolved: true, batch_s: 0, ignore_quiet: false};
    switch (source) {
        case 'warning':
            return {...r, batch_s: 60, severities: ['error', 'warning']};
        case 'service':
            return {...r, batch_s: 60, hold_s: 120, kinds: ['unreach', 'fault', 'lowbat', 'other']};
        case 'datapoint':
            return {...r, datapoint: {interface: '', address: '', key: '', op: 'true', value: '', for_s: 0, label: ''}};
        case 'system':
            return {...r, kinds: ['update-available', 'update-installed', 'backup-failed']};
        case 'inbound':
            return {...r, tag: ''};
        case 'digest':
            return {...r, resolved: false, priority: 'low', time: '08:00', only_open: false};
    }
}

/** the port a channel uses when the field is empty */
export function defaultPort(ch: Channel): number | undefined {
    if (ch.type === 'email') return {starttls: 587, tls: 465, none: 25}[ch.security ?? 'starttls'];
    if (ch.type === 'mqtt') return ch.security === 'tls' ? 8883 : 1883;
    return undefined;
}

/** whether the form may be saved or tested: every required field has a value, or its secret is stored */
export function channelComplete(ch: Channel): boolean {
    if (ch.name.trim() === '') return false;
    return FIELDS[ch.type].every((f) => {
        if (!f.required) return true;
        const v = ch[f.name];
        if (typeof v === 'string' && v.trim() !== '') return true;
        return f.kind === 'secret' && ch.secrets?.[f.name] === true && !(ch.clear ?? []).includes(f.name);
    });
}

/** where a channel sends to, in a few words, without anything secret */
export function channelTarget(ch: Channel): string {
    switch (ch.type) {
        case 'pushover':
            return ch.target ?? '';
        case 'telegram':
            return ch.target ?? '';
        case 'email':
            return `${ch.target ?? ''} · ${ch.host ?? ''}:${ch.port ?? defaultPort(ch)}`;
        case 'ntfy':
            return `${ch.url ?? 'https://ntfy.sh'}/${ch.target ?? ''}`;
        case 'gotify':
        case 'webhook':
            return ch.url ?? '';
        case 'signal':
            return `${ch.target ?? ''} · ${ch.url ?? ''}`;
        case 'mqtt':
            return `${ch.host || '127.0.0.1'}:${ch.port ?? defaultPort(ch)} · ${ch.target ?? ''}`;
        default:
            return '';
    }
}

/** seconds as the page says them: 90 s, 5 min, 2 h */
export function seconds(s: number): string {
    if (s < 120) return `${s} s`;
    if (s < 7200) return `${Math.round(s / 60)} min`;
    return `${Math.round(s / 3600)} h`;
}

/** what a rule filters by, as the list shows it; t translates the filter's words */
export function ruleFilter(r: Rule, t: (key: string, params?: Record<string, string | number>) => string): string {
    switch (r.source) {
        case 'warning':
            return (r.severities ?? []).map((s) => t('severity.' + s)).join(', ') + (r.exclude_ids?.length ? ' · ' + t('without {list}', {list: r.exclude_ids.join(', ')}) : '');
        case 'service':
            return (r.kinds ?? []).map((k) => t('kind.' + k)).join(', ') + (r.hold_s ? ' · ' + t('after {time}', {time: seconds(r.hold_s)}) : '');
        case 'system':
            return (r.kinds ?? []).map((k) => t('system.' + k)).join(', ');
        case 'datapoint': {
            const d = r.datapoint;
            if (!d) return '';
            const cond = d.op === 'true' || d.op === 'false' ? t('op.' + d.op) : `${t('op.' + d.op)} ${d.value ?? ''}`;
            return `${d.address} ${d.key} ${cond}` + (d.for_s ? ' · ' + t('for {time}', {time: seconds(d.for_s)}) : '');
        }
        case 'inbound':
            return r.tag ? t('source {tag}', {tag: r.tag}) : t('every sender');
        case 'digest':
            return t('daily at {time}', {time: r.time ?? ''}) + (r.only_open ? ' · ' + t('only when something is open') : '');
    }
}

/** the channels a rule sends to, by name */
export function ruleChannels(r: Rule, channels: Channel[], t: (key: string) => string): string {
    if (r.channels.length === 0) return t('every channel');
    return r.channels.map((id) => channels.find((c) => c.id === id)?.name ?? id).join(', ');
}

/** the headers of a webhook as the form edits them, and back */
export function headerRows(h: Record<string, string> | undefined): {name: string; value: string}[] {
    return Object.entries(h ?? {}).map(([name, value]) => ({name, value}));
}

export function headersOf(rows: {name: string; value: string}[]): Record<string, string> {
    const out: Record<string, string> = {};
    for (const r of rows) if (r.name.trim() !== '') out[r.name.trim()] = r.value;
    return out;
}

/** the body a save sends: no id-less noise, numbers as numbers, the secrets flags left out */
export function channelBody(ch: Channel): Channel {
    const b: Channel = {...ch};
    delete b.secrets;
    if (b.port === undefined || b.port === null || Number.isNaN(b.port) || (b.port as unknown) === '') delete b.port;
    if (b.qos !== undefined) b.qos = Number(b.qos);
    if (!b.clear?.length) delete b.clear;
    return b;
}

/** a time as the page shows it */
export function clock(iso: string | undefined, lang: string): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
    return d.toLocaleString(lang === 'de' ? 'de-DE' : 'en-GB', {day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit'});
}
