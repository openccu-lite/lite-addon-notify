<script lang="ts">
    // Adds a rule or changes one: the source's filter, the channels, how it is delivered, and
    // the title and body templates with a live preview on sample events.
    import {untrack} from 'svelte';
    import {Dialog} from 'occulite-ui';
    import {i18n, t} from 'occulite-ui/i18n';

    import {ApiError, get, post, put} from './api.js';
    import {emptyRule, OPS, PRIORITIES, SERVICE_KINDS, SEVERITIES, SYSTEM_KINDS, type Config, type Device, type Rule, type Source} from './model.js';

    let {open, rule, source, config, onclose, onsaved}: {open: boolean; rule: Rule | undefined; source: Source | undefined; config: Config; onclose: () => void; onsaved: (c: Config) => void} = $props();

    let draft = $state<Rule>(emptyRule('warning'));
    let error = $state('');
    let busy = $state(false);
    let allChannels = $state(true);
    let excludeIds = $state('');
    let exclude = $state('');
    let devices = $state<Device[] | undefined>();
    let devicesError = $state('');
    let preview = $state<{title: string; body: string} | undefined>();
    let previewError = $state('');
    let templates = $state(false);

    // the form starts from the rule (or an empty one of the source) whenever the dialog opens;
    // what it writes is not what it depends on
    $effect(() => {
        if (!open) return;
        const from = rule;
        const kind = source;
        untrack(() => {
            const base = from ? structuredClone($state.snapshot(from)) : emptyRule(kind ?? 'warning');
            if (!from) base.name = t('rule.' + base.source);
            if (base.source === 'datapoint' && !base.datapoint) base.datapoint = {interface: '', address: '', key: '', op: 'true'};
            // the API leaves out what is zero or empty; the form shows it
            if (base.datapoint) base.datapoint = {value: '', for_s: 0, label: '', ...base.datapoint};
            if (base.source === 'service') base.hold_s ??= 0;
            draft = base;
            allChannels = base.channels.length === 0;
            excludeIds = (base.exclude_ids ?? []).join(', ');
            exclude = (base.exclude ?? []).join(', ');
            templates = !!(base.title || base.body);
            error = '';
            preview = undefined;
            previewError = '';
            if (base.source === 'datapoint') void loadDevices();
        });
    });

    async function loadDevices() {
        try {
            devices = await get<Device[]>('api/v1/devices');
            devicesError = '';
        } catch (e) {
            devices = [];
            devicesError = (e as Error).message;
        }
    }

    const isNew = $derived(rule === undefined);
    const list = (s: string) =>
        s
            .split(/[,\s]+/)
            .map((x) => x.trim())
            .filter(Boolean);

    function body(): Rule {
        const b: Rule = {...$state.snapshot(draft), name: draft.name.trim(), channels: allChannels ? [] : [...draft.channels]};
        if (b.source === 'warning') b.exclude_ids = list(excludeIds);
        if (b.source === 'service') b.exclude = list(exclude);
        if (!templates) {
            b.title = '';
            b.body = '';
        }
        b.batch_s = Number(b.batch_s) || 0;
        if (b.hold_s !== undefined) b.hold_s = Number(b.hold_s) || 0;
        if (b.datapoint) b.datapoint = {...b.datapoint, for_s: Number(b.datapoint.for_s) || 0};
        return b;
    }

    function toggle(arr: string[] | undefined, v: string, on: boolean): string[] {
        const s = new Set(arr ?? []);
        if (on) s.add(v);
        else s.delete(v);
        return [...s];
    }

    const dp = $derived(draft.datapoint);
    const device = $derived(devices?.find((d) => d.interface === dp?.interface && d.address === dp?.address));
    const needsValue = $derived(dp !== undefined && dp.op !== 'true' && dp.op !== 'false');
    const valid = $derived.by(() => {
        if (draft.name.trim() === '') return false;
        if (!allChannels && draft.channels.length === 0) return false;
        switch (draft.source) {
            case 'warning':
                return (draft.severities ?? []).length > 0;
            case 'service':
            case 'system':
                return (draft.kinds ?? []).length > 0;
            case 'datapoint':
                return !!dp && dp.interface !== '' && dp.address !== '' && dp.key !== '' && (!needsValue || (dp.value ?? '') !== '');
            case 'digest':
                return /^\d\d:\d\d$/.test(draft.time ?? '');
        }
        return true;
    });

    function pickDevice(ref: string) {
        const d = devices?.find((x) => `${x.interface}|${x.address}` === ref);
        if (!d || !draft.datapoint) return;
        draft.datapoint.interface = d.interface;
        draft.datapoint.address = d.address;
        if (!d.keys.some((k) => k.key === draft.datapoint!.key)) draft.datapoint.key = d.keys[0]?.key ?? '';
    }

    // the preview follows the form, a moment after the last keystroke
    $effect(() => {
        if (!open || !valid) return;
        const b = body();
        const timer = setTimeout(async () => {
            try {
                preview = await post<{title: string; body: string}>('api/v1/rules/preview', b);
                previewError = '';
            } catch (e) {
                const x = e instanceof ApiError ? (e.body as {message?: string}) : undefined;
                previewError = x?.message ?? (e as Error).message;
            }
        }, 300);
        return () => clearTimeout(timer);
    });

    async function save() {
        busy = true;
        error = '';
        try {
            const c = isNew ? await post<Config>('api/v1/rules', body()) : await put<Config>(`api/v1/rules/${encodeURIComponent(rule!.id)}`, body());
            onsaved(c);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
</script>

<Dialog {open} title={isNew ? t('New rule: {source}', {source: t('rule.' + draft.source)}) : t('Rule {name}', {name: rule?.name ?? ''})} width="46rem" closable={!busy} {onclose} testId="rule-dialog">
    <div class="nfy-form">
        <label for="r-name">{t('Name')}</label>
        <input class="hmm-input" id="r-name" type="text" bind:value={draft.name} maxlength="64" data-testid="r-name" />

        {#if draft.source === 'warning'}
            <span>{t('Severities')}</span>
            <div class="nfy-checks">
                {#each SEVERITIES as s (s)}
                    <label class="nfy-check"><input type="checkbox" checked={(draft.severities ?? []).includes(s)} onchange={(e) => (draft.severities = toggle(draft.severities, s, e.currentTarget.checked))} data-testid="r-sev-{s}" /> {t('severity.' + s)}</label>
                {/each}
            </div>
            <label for="r-exclude-ids">{t('Leave out (warning ids)')}</label>
            <input class="hmm-input" id="r-exclude-ids" type="text" bind:value={excludeIds} list="warning-ids" placeholder="backup-unencrypted, journal-ram" spellcheck="false" data-testid="r-exclude-ids" />
            <datalist id="warning-ids">
                {#each config.warning_ids as id (id)}<option value={id}></option>{/each}
            </datalist>
            <p class="nfy-help">{t('The Status page shows a warning\'s id in its address when you follow its link; the usual ones are suggested.')}</p>
        {:else if draft.source === 'service'}
            <span>{t('Kinds')}</span>
            <div class="nfy-checks">
                {#each SERVICE_KINDS as k (k)}
                    <label class="nfy-check"><input type="checkbox" checked={(draft.kinds ?? []).includes(k)} onchange={(e) => (draft.kinds = toggle(draft.kinds, k, e.currentTarget.checked))} data-testid="r-kind-{k}" /> {t('kind.' + k)}</label>
                {/each}
            </div>
            <label for="r-hold">{t('Only after (s)')}</label>
            <input class="hmm-input nfy-narrow" id="r-hold" type="number" min="0" max="86400" bind:value={draft.hold_s} data-testid="r-hold" />
            <p class="nfy-help">{t('A message must stand that long before it counts: a device that answers again within the time is never reported.')}</p>
            <label for="r-exclude">{t('Leave out (device addresses)')}</label>
            <input class="hmm-input" id="r-exclude" type="text" bind:value={exclude} spellcheck="false" placeholder="000A1BE9A1B2C3" data-testid="r-exclude" />
        {:else if draft.source === 'system'}
            <span>{t('Events')}</span>
            <div class="nfy-checks">
                {#each SYSTEM_KINDS as k (k)}
                    <label class="nfy-check"><input type="checkbox" checked={(draft.kinds ?? []).includes(k)} onchange={(e) => (draft.kinds = toggle(draft.kinds, k, e.currentTarget.checked))} data-testid="r-sys-{k}" /> {t('system.' + k)}</label>
                {/each}
            </div>
        {:else if draft.source === 'datapoint' && dp}
            <label for="r-device">{t('Device or channel')}</label>
            <select class="hmm-select" id="r-device" value={device ? `${device.interface}|${device.address}` : ''} onchange={(e) => pickDevice(e.currentTarget.value)} data-testid="r-device">
                <option value="">{devices === undefined ? t('Loading…') : t('choose, or enter below')}</option>
                {#each devices ?? [] as d (d.interface + d.address)}
                    <option value="{d.interface}|{d.address}">{d.name || d.address}{d.rooms.length ? ' (' + d.rooms.join(', ') + ')' : ''} · {d.address}</option>
                {/each}
            </select>
            {#if devicesError}<p class="nfy-help nfy-error-inline">{devicesError}</p>{/if}
            <label for="r-iface">{t('Interface')}</label>
            <input class="hmm-input" id="r-iface" type="text" bind:value={dp.interface} placeholder="HmIP-RF" spellcheck="false" data-testid="r-iface" />
            <label for="r-address">{t('Address')}</label>
            <input class="hmm-input" id="r-address" type="text" bind:value={dp.address} placeholder="0000D3C99C6B41:1" spellcheck="false" data-testid="r-address" />
            <label for="r-key">{t('Datapoint')}</label>
            <input class="hmm-input" id="r-key" type="text" bind:value={dp.key} list="dp-keys" placeholder="STATE" spellcheck="false" data-testid="r-key" />
            <datalist id="dp-keys">
                {#each device?.keys ?? [] as k (k.key)}<option value={k.key}>{String(k.value)}</option>{/each}
            </datalist>
            <p class="nfy-help">{t('The list holds the datapoints the system keeps values of; any other datapoint of the device can be entered and is judged from its next event on.')}</p>
            <label for="r-op">{t('Condition')}</label>
            <div class="nfy-cond">
                <select class="hmm-select" id="r-op" bind:value={dp.op} data-testid="r-op">
                    {#each OPS as o (o)}<option value={o}>{t('op.' + o)}</option>{/each}
                </select>
                <input class="hmm-input" type="text" bind:value={dp.value} disabled={!needsValue} aria-label={t('Value')} data-testid="r-value" />
            </div>
            <label for="r-for">{t('For at least (s)')}</label>
            <input class="hmm-input nfy-narrow" id="r-for" type="number" min="0" max="604800" bind:value={dp.for_s} data-testid="r-for" />
            <p class="nfy-help">{t('0 reports at once; 600 reports a window that has been open for ten minutes.')}</p>
            <label for="r-label">{t('The message says')}</label>
            <input class="hmm-input" id="r-label" type="text" bind:value={dp.label} maxlength="120" placeholder={t('e.g. open for more than 10 minutes')} data-testid="r-label" />
        {:else if draft.source === 'inbound'}
            <label for="r-tag">{t('Sender\'s source')}</label>
            <input class="hmm-input" id="r-tag" type="text" bind:value={draft.tag} placeholder={t('empty: every sender')} spellcheck="false" data-testid="r-tag" />
            <p class="nfy-help">{t('What the sender gives as "source" (in the JSON body or as ?source= in the URL). The inbound webhook is switched on in the settings.')}</p>
        {:else if draft.source === 'digest'}
            <label for="r-time">{t('Time of day')}</label>
            <input class="hmm-input nfy-time" id="r-time" type="time" bind:value={draft.time} data-testid="r-time" />
            <label class="nfy-check"><input type="checkbox" bind:checked={draft.only_open} data-testid="r-only-open" /> {t('Only when something is open (no "all clear")')}</label>
        {/if}

        <span>{t('Channels')}</span>
        <div>
            <label class="nfy-check"><input type="checkbox" bind:checked={allChannels} data-testid="r-all-channels" /> {t('Every enabled channel')}</label>
            {#if !allChannels}
                <div class="nfy-checks">
                    {#each config.channels as ch (ch.id)}
                        <label class="nfy-check"><input type="checkbox" checked={draft.channels.includes(ch.id)} onchange={(e) => (draft.channels = toggle(draft.channels, ch.id, e.currentTarget.checked))} data-testid="r-channel-{ch.id}" /> {ch.name}</label>
                    {/each}
                    {#if config.channels.length === 0}<span class="nfy-muted">{t('No channel yet.')}</span>{/if}
                </div>
            {/if}
        </div>

        <label for="r-priority">{t('Priority')}</label>
        <select class="hmm-select" id="r-priority" bind:value={draft.priority} data-testid="r-priority">
            {#each PRIORITIES as p (p)}<option value={p}>{t('priority.' + p)}</option>{/each}
        </select>
        {#if draft.source !== 'digest'}
            <label for="r-batch">{t('Batching window (s)')}</label>
            <input class="hmm-input nfy-narrow" id="r-batch" type="number" min="0" max="3600" bind:value={draft.batch_s} data-testid="r-batch" />
            <p class="nfy-help">{t('What happens within the window goes out as one message; 0 sends every event at once, as its own message.')}</p>
            <label class="nfy-check"><input type="checkbox" bind:checked={draft.resolved} data-testid="r-resolved" /> {t('Also say when it is over ("resolved")')}</label>
            <label class="nfy-check"><input type="checkbox" bind:checked={draft.ignore_quiet} data-testid="r-ignore-quiet" /> {t('Send during the quiet hours too')}</label>
        {/if}
        <label class="nfy-check"><input type="checkbox" bind:checked={templates} data-testid="r-templates" /> {t('Own title and body (templates)')}</label>
        {#if templates}
            <label for="r-title">{t('Title')}</label>
            <input class="hmm-input nfy-mono" id="r-title" type="text" bind:value={draft.title} placeholder={'{{ headline }}'} spellcheck="false" data-testid="r-title" />
            <label for="r-body">{t('Body')}</label>
            <textarea class="hmm-input nfy-mono" id="r-body" rows="4" bind:value={draft.body} placeholder={'{{ lines | join("\\n") }}'} spellcheck="false" data-testid="r-body"></textarea>
            <p class="nfy-help">
                {t('Jinja syntax. Variables: system, kind, rule, resolved, count, headline, lines, items (title, text, line, detail, device, address, rooms, datapoint, value, severity, since, resolved), priority, link, time. An empty field keeps the default.')}
                {config.filters.map((f) => f.name).join(', ')}.
            </p>
        {/if}
        <div class="nfy-preview-box" data-testid="r-preview" lang={i18n.language}>
            <span class="nfy-faint">{t('Preview (sample events)')}</span>
            {#if previewError}
                <span class="nfy-error-inline" data-testid="r-preview-error">{previewError}</span>
            {:else if preview}
                <strong data-testid="r-preview-title">{preview.title}</strong>
                <pre data-testid="r-preview-body">{preview.body}</pre>
            {/if}
        </div>
        <label class="nfy-check"><input type="checkbox" bind:checked={draft.enabled} data-testid="r-enabled" /> {t('Enabled')}</label>
    </div>
    <p class="nfy-error" role="alert" data-testid="r-error">{error}</p>
    {#snippet buttons()}
        <button class="hmm-button" onclick={onclose} disabled={busy}>{t('Cancel')}</button>
        <button class="hmm-button nfy-primary" onclick={save} disabled={!valid || busy} data-testid="r-save">{t('Save')}</button>
    {/snippet}
</Dialog>
