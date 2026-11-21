<script lang="ts">
    // The last deliveries: what went to which channel, and why one failed.
    import {onMount} from 'svelte';
    import {i18n, t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import ConfirmDialog from './ConfirmDialog.svelte';
    import {del, get} from './api.js';
    import {clock, type HistoryRow} from './model.js';

    let {notices}: {notices: NoticesStore} = $props();

    let rows = $state<HistoryRow[] | undefined>();
    let onlyFailed = $state(false);
    let clearing = $state(false);
    let open = $state<string | undefined>();

    async function load() {
        try {
            rows = await get<HistoryRow[]>('api/v1/history');
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    onMount(() => {
        void load();
        const poll = setInterval(() => void load(), 5000);
        return () => clearInterval(poll);
    });

    async function clear() {
        clearing = false;
        try {
            await del('api/v1/history');
            await load();
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    const shown = $derived((rows ?? []).filter((r) => !onlyFailed || (r.result !== 'sent' && r.result !== 'limited')));
    const cls = (result: string) => (result === 'sent' ? 'ok' : result === 'limited' || result === 'failed' ? 'warn' : 'bad');
    const id = (r: HistoryRow, i: number) => `${r.time}/${r.channel}/${r.message}/${r.attempt ?? 0}/${i}`;
</script>

<section class="nfy-section nfy-section-wide" data-testid="history">
    <div class="nfy-section-head">
        <h2>{t('History')}</h2>
        <label class="nfy-check"><input type="checkbox" bind:checked={onlyFailed} data-testid="only-failed" /> {t('Only what did not arrive')}</label>
        <button class="hmm-button" disabled={!rows?.length} onclick={() => (clearing = true)} data-testid="history-clear">{t('Empty')}</button>
    </div>
    {#if rows === undefined}
        <p class="nfy-muted">{t('Loading…')}</p>
    {:else if shown.length === 0}
        <p class="nfy-muted" data-testid="history-empty">{t('Nothing sent yet.')}</p>
    {:else}
        <table class="nfy-grid">
            <thead>
                <tr><th>{t('Time')}</th><th>{t('Channel')}</th><th>{t('Rule')}</th><th>{t('Message')}</th><th>{t('Result')}</th></tr>
            </thead>
            <tbody>
                {#each shown as r, i (id(r, i))}
                    <tr data-testid="history-row">
                        <td class="nfy-nowrap">{clock(r.time, i18n.language)}</td>
                        <td>{r.channel_name || r.channel}</td>
                        <td>{r.rule_name || (r.kind === 'test' ? t('Test') : '')}</td>
                        <td>
                            <button class="nfy-linklike" onclick={() => (open = open === id(r, i) ? undefined : id(r, i))}>{r.title}</button>
                            {#if open === id(r, i) && r.body}<pre class="nfy-body">{r.body}</pre>{/if}
                            {#if r.error}<div class="nfy-error-inline nfy-small">{r.step ? t('step.' + r.step) + ': ' : ''}{r.error}</div>{/if}
                        </td>
                        <td><span class={'nfy-pill ' + cls(r.result)} title={t('result.' + r.result)}>{t('result.' + r.result)}</span>{#if (r.attempt ?? 0) > 1}<span class="nfy-faint"> {t('attempt {n}', {n: r.attempt ?? 0})}</span>{/if}</td>
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
    <p class="nfy-help">{t('The last 200 deliveries. A failed send is tried again after 10 s, 1 min, 5 min and 15 min.')}</p>
</section>

<ConfirmDialog open={clearing} title={t('Empty the history')} message={t('The list of deliveries is emptied. Nothing else changes.')} confirmLabel={t('Empty')} onconfirm={clear} onclose={() => (clearing = false)} />
