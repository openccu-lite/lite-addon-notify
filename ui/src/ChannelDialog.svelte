<script lang="ts">
    // Adds a channel or changes one. The form is built from the type's fields; a secret is
    // write-only: an empty field on a saved channel keeps the stored one. The Test button sends
    // a test message with what the form holds, saved or not, and shows the target's own answer.
    import {untrack} from 'svelte';
    import {Dialog} from 'occulite-ui';
    import {t} from 'occulite-ui/i18n';

    import {ApiError, post, put} from './api.js';
    import {channelBody, channelComplete, defaultPort, emptyChannel, FIELDS, headerRows, headersOf, TYPE_NAMES, type Channel, type ChannelType, type Config, type Field} from './model.js';

    let {open, channel, type, onclose, onsaved}: {open: boolean; channel: Channel | undefined; type: ChannelType | undefined; onclose: () => void; onsaved: (c: Config) => void} = $props();

    let draft = $state<Channel>(emptyChannel('ntfy'));
    let headers = $state<{name: string; value: string}[]>([]);
    let error = $state('');
    let result = $state<{ok: boolean; text: string} | undefined>();
    let busy = $state(false);
    let testing = $state(false);

    // the form starts from the channel (or an empty one of the type) whenever the dialog opens;
    // what it writes is not what it depends on
    $effect(() => {
        if (!open) return;
        const from = channel;
        const kind = type;
        untrack(() => {
            const d = from ? {...$state.snapshot(from), qos: from.qos ?? 0} : emptyChannel(kind ?? 'ntfy');
            if (!from) d.name = TYPE_NAMES[d.type];
            draft = d;
            headers = headerRows(from?.headers);
            error = '';
            result = undefined;
        });
    });

    const isNew = $derived(channel === undefined);
    const fields = $derived(FIELDS[draft.type]);
    const basic = $derived(fields.filter((f) => !f.advanced));
    const advanced = $derived(fields.filter((f) => f.advanced));
    const valid = $derived(channelComplete(draft));

    function body(): Channel {
        const b = channelBody({...draft, name: draft.name.trim()});
        if (draft.type === 'webhook') b.headers = headersOf(headers);
        return b;
    }

    function value(f: Field): string {
        const v = draft[f.name];
        return v === undefined || v === null ? '' : String(v);
    }

    function set(f: Field, v: string) {
        const d = draft as unknown as Record<string, unknown>;
        if (f.kind === 'number') d[f.name] = v === '' ? undefined : Number(v);
        else if (f.name === 'qos') d[f.name] = Number(v);
        else d[f.name] = v;
    }

    function stored(f: Field): boolean {
        return draft.secrets?.[f.name] === true;
    }

    async function test() {
        testing = true;
        error = '';
        result = undefined;
        try {
            await post('api/v1/channels/test', body());
            result = {ok: true, text: t('The test message went out. Did it arrive?')};
        } catch (e) {
            const b = e instanceof ApiError ? (e.body as {step?: string; message?: string; field?: string}) : undefined;
            result = {ok: false, text: (b?.step ? t('step.' + b.step) + ': ' : '') + (b?.message ?? (e as Error).message)};
        } finally {
            testing = false;
        }
    }

    async function save() {
        busy = true;
        error = '';
        try {
            const c = isNew ? await post<Config>('api/v1/channels', body()) : await put<Config>(`api/v1/channels/${encodeURIComponent(channel!.id)}`, body());
            onsaved(c);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
</script>

{#snippet field(f: Field)}
    {#if f.kind === 'check'}
        <label class="nfy-check"><input type="checkbox" checked={draft[f.name] === true} onchange={(e) => ((draft as unknown as Record<string, unknown>)[f.name] = e.currentTarget.checked)} data-testid="ch-{f.name}" /> {t(f.label)}</label>
    {:else if f.kind === 'headers'}
        <span>{t(f.label)}</span>
        <div>
            <div class="nfy-headers">
                {#each headers as h, i (i)}
                    <input class="hmm-input" type="text" bind:value={h.name} placeholder="Authorization" aria-label={t('Header name')} data-testid="ch-header-name-{i}" />
                    <input class="hmm-input" type="password" bind:value={h.value} autocomplete="new-password" placeholder={stored(f) && channel?.headers && h.name in channel.headers ? t('unchanged') : ''} aria-label={t('Header value')} data-testid="ch-header-value-{i}" />
                    <button class="hmm-button" onclick={() => (headers = headers.filter((_, j) => j !== i))} aria-label={t('Remove')}>×</button>
                {/each}
            </div>
            <button class="hmm-button" onclick={() => (headers = [...headers, {name: '', value: ''}])} data-testid="ch-header-add">{t('Add header')}</button>
        </div>
    {:else}
        <label for="ch-{f.name}">{t(f.label)}</label>
        {#if f.kind === 'select'}
            <select class="hmm-select" id="ch-{f.name}" value={value(f)} onchange={(e) => set(f, e.currentTarget.value)} data-testid="ch-{f.name}">
                {#each f.options ?? [] as o (o.value)}
                    <option value={o.value}>{t(o.label)}</option>
                {/each}
            </select>
        {:else if f.kind === 'area'}
            <textarea class="hmm-input nfy-mono" id="ch-{f.name}" rows="4" spellcheck="false" value={value(f)} oninput={(e) => set(f, e.currentTarget.value)} data-testid="ch-{f.name}"></textarea>
        {:else if f.kind === 'secret'}
            <input class="hmm-input" id="ch-{f.name}" type="password" autocomplete="new-password" value={value(f)} oninput={(e) => set(f, e.currentTarget.value)} placeholder={stored(f) ? t('unchanged') : ''} data-testid="ch-{f.name}" />
        {:else if f.kind === 'number'}
            <input class="hmm-input nfy-narrow" id="ch-{f.name}" type="number" min="1" max="65535" value={value(f)} oninput={(e) => set(f, e.currentTarget.value)} placeholder={String(defaultPort(draft) ?? '')} data-testid="ch-{f.name}" />
        {:else}
            <input class="hmm-input" id="ch-{f.name}" type="text" autocomplete="off" spellcheck="false" value={value(f)} oninput={(e) => set(f, e.currentTarget.value)} placeholder={f.placeholder ?? ''} data-testid="ch-{f.name}" />
        {/if}
    {/if}
    {#if f.help}<p class="nfy-help">{t(f.help)}</p>{/if}
{/snippet}

<Dialog {open} title={isNew ? t('New channel: {type}', {type: TYPE_NAMES[draft.type]}) : t('Channel {name}', {name: channel?.name ?? ''})} width="40rem" closable={!busy} {onclose} testId="channel-dialog">
    <div class="nfy-form">
        <label for="ch-name">{t('Name')}</label>
        <input class="hmm-input" id="ch-name" type="text" bind:value={draft.name} maxlength="64" data-testid="ch-name" />
        {#each basic as f (f.name)}
            {@render field(f)}
        {/each}
        {#if advanced.length}
            <details class="nfy-more">
                <summary data-testid="ch-more">{t('More settings')}</summary>
                <div class="nfy-form-inner">
                    {#each advanced as f (f.name)}
                        {@render field(f)}
                    {/each}
                </div>
            </details>
        {/if}
        <label class="nfy-check"><input type="checkbox" bind:checked={draft.enabled} data-testid="ch-enabled" /> {t('Enabled')}</label>
    </div>
    <div class="nfy-row nfy-probe">
        <button class="hmm-button nfy-fixed" onclick={test} disabled={!valid || testing || busy} data-testid="ch-test">{testing ? t('Testing…') : t('Send a test')}</button>
        <p class={'nfy-result ' + (result ? (result.ok ? 'nfy-ok' : 'nfy-error-inline') : '')} role="status" data-testid="ch-result">{result?.text ?? ''}</p>
    </div>
    <p class="nfy-error" role="alert" data-testid="ch-error">{error}</p>
    {#snippet buttons()}
        <button class="hmm-button" onclick={onclose} disabled={busy}>{t('Cancel')}</button>
        <button class="hmm-button nfy-primary" onclick={save} disabled={!valid || busy} data-testid="ch-save">{t('Save')}</button>
    {/snippet}
</Dialog>
