<script lang="ts">
    // The general settings: the messages' language and names, quiet hours, repeat suppression,
    // the rate limit, and the inbound webhook with its address and secret.
    import {onMount, untrack} from 'svelte';
    import {copyText} from 'occulite-ui/clipboard';
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import ConfirmDialog from './ConfirmDialog.svelte';
    import {get, post, put} from './api.js';
    import type {Config, InboundInfo, Settings} from './model.js';

    let {config, notices, onchanged}: {config: Config; notices: NoticesStore; onchanged: (c: Config) => void} = $props();

    const copy = (s: Settings): Settings => ({...s, quiet: {...s.quiet}, inbound: {...s.inbound}});
    let form = $state<Settings>(untrack(() => copy(config.settings)));
    let busy = $state(false);
    let inbound = $state<InboundInfo | undefined>();
    let shown = $state(false);
    let rotating = $state(false);

    $effect(() => {
        form = copy(config.settings);
    });

    const dirty = $derived(JSON.stringify(form) !== JSON.stringify(config.settings));

    async function loadInbound() {
        try {
            inbound = await get<InboundInfo>('api/v1/inbound');
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    onMount(() => void loadInbound());

    async function save() {
        busy = true;
        try {
            const body = {...form, repeat_min: Number(form.repeat_min) || 0, rate_per_hour: Number(form.rate_per_hour) || 0};
            onchanged(await put<Config>('api/v1/settings', body));
            notices.push('info', t('Saved.'));
            await loadInbound();
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = false;
        }
    }

    async function rotate() {
        rotating = false;
        try {
            inbound = await post<InboundInfo>('api/v1/inbound/rotate');
            shown = true;
            notices.push('info', t('A new secret: the old one no longer works.'));
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    async function copySecret() {
        if (!inbound) return;
        const out = await copyText(inbound.secret);
        notices.push(out === 'failed' ? 'error' : 'info', out === 'failed' ? t('Copying failed: show the secret and copy it by hand.') : t('Copied.'));
    }

    const curl = $derived(inbound ? `curl -s -H "Authorization: Bearer ${shown ? inbound.secret : '<secret>'}" -H "Content-Type: application/json" \\\n  -d '{"title":"Hello","text":"from a script","source":"script"}' ${inbound.url}` : '');
</script>

<section class="nfy-section" data-testid="settings">
    <h2>{t('Messages')}</h2>
    <div class="nfy-form">
        <label for="s-lang">{t('Language of the messages')}</label>
        <select class="hmm-select" id="s-lang" bind:value={form.language} data-testid="s-language">
            <option value="de">Deutsch</option>
            <option value="en">English</option>
        </select>
        <label for="s-name">{t('Name of the system')}</label>
        <input class="hmm-input" id="s-name" type="text" bind:value={form.system_name} maxlength="64" placeholder={config.system} data-testid="s-system-name" />
        <label class="nfy-check"><input type="checkbox" bind:checked={form.title_prefix} data-testid="s-title-prefix" /> {t('Put the system\'s name in front of every title')}</label>
        <label for="s-url">{t('Address of the system')}</label>
        <input class="hmm-input" id="s-url" type="text" bind:value={form.base_url} placeholder="https://ccu.example.org" spellcheck="false" data-testid="s-base-url" />
        <p class="nfy-help">{t('As your phone reaches the system. With it, a message links to the system\'s page; without it, messages carry no link.')}</p>
    </div>

    <h2>{t('Quiet hours')}</h2>
    <div class="nfy-form">
        <label class="nfy-check"><input type="checkbox" bind:checked={form.quiet.enabled} data-testid="s-quiet" /> {t('Hold messages back during the quiet hours')}</label>
        <label for="s-from">{t('From')}</label>
        <input class="hmm-input nfy-time" id="s-from" type="time" bind:value={form.quiet.from} disabled={!form.quiet.enabled} data-testid="s-quiet-from" />
        <label for="s-to">{t('Until')}</label>
        <input class="hmm-input nfy-time" id="s-to" type="time" bind:value={form.quiet.to} disabled={!form.quiet.enabled} data-testid="s-quiet-to" />
        <p class="nfy-help">{t('What happens meanwhile arrives as one message per rule when they end, without what was over by then. A rule can ignore the quiet hours.')}</p>
    </div>

    <h2>{t('Limits')}</h2>
    <div class="nfy-form">
        <label for="s-repeat">{t('Repeat suppression (min)')}</label>
        <input class="hmm-input nfy-narrow" id="s-repeat" type="number" min="0" max="10080" bind:value={form.repeat_min} data-testid="s-repeat" />
        <p class="nfy-help">{t('The same matter is not announced again within this time; if it still stands when the time is over, it is announced then. 0 switches it off.')}</p>
        <label for="s-rate">{t('Messages per channel and hour')}</label>
        <input class="hmm-input nfy-narrow" id="s-rate" type="number" min="0" max="3600" bind:value={form.rate_per_hour} data-testid="s-rate" />
        <p class="nfy-help">{t('More than that are held back and counted; the next message that passes says how many. 0 means no limit.')}</p>
    </div>

    <h2>{t('Inbound webhook')}</h2>
    <div class="nfy-form">
        <label class="nfy-check"><input type="checkbox" bind:checked={form.inbound.enabled} data-testid="s-inbound" /> {t('Accept messages from other programs on this system')}</label>
        <p class="nfy-help">{t('Other addons, a Node-RED flow or a script on the system send a message to the address below; a rule of the kind "Inbound webhook" takes it to your channels. The address answers on the system itself only, never from the network.')}</p>
        {#if inbound}
            <span>{t('Address')}</span>
            <span class="nfy-code" data-testid="inbound-url">{inbound.url}</span>
            <span>{t('Secret')}</span>
            <span class="nfy-secret-row">
                <input class="hmm-input nfy-mono" type={shown ? 'text' : 'password'} readonly value={inbound.secret} aria-label={t('Secret')} data-testid="inbound-secret" />
                <button class="hmm-button nfy-switch" onclick={() => (shown = !shown)} data-testid="inbound-show">{shown ? t('Hide') : t('Show')}</button>
                <button class="hmm-button" onclick={copySecret} data-testid="inbound-copy">{t('Copy')}</button>
                <button class="hmm-button" onclick={() => (rotating = true)} data-testid="inbound-rotate">{t('New secret')}</button>
            </span>
            <span>{t('Example')}</span>
            <pre class="nfy-body nfy-mono" data-testid="inbound-curl">{curl}</pre>
            <p class="nfy-help">{t('The secret goes as "Authorization: Bearer", as the header X-Notify-Secret or as ?secret= in the address. The body is JSON (title, text, priority, source, key, resolved) or plain text with ?title=. At most {n} messages a minute.', {n: inbound.per_minute})}</p>
        {/if}
    </div>

    <div class="nfy-actions">
        <button class="hmm-button nfy-primary" disabled={!dirty || busy} onclick={save} data-testid="settings-save">{t('Save')}</button>
        <button class="hmm-button" disabled={!dirty || busy} onclick={() => (form = copy(config.settings))}>{t('Discard')}</button>
    </div>
</section>

<ConfirmDialog open={rotating} title={t('New secret')} message={t('Every program that sends with the old secret stops working until it has the new one.')} confirmLabel={t('New secret')} danger onconfirm={rotate} onclose={() => (rotating = false)} />
