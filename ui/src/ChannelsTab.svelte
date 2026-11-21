<script lang="ts">
    // The channels: where messages go. Add one by its type, test it, switch it off, remove it.
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import ChannelDialog from './ChannelDialog.svelte';
    import ConfirmDialog from './ConfirmDialog.svelte';
    import {ApiError, del, post, put} from './api.js';
    import {channelBody, channelTarget, TYPE_NAMES, type Channel, type ChannelType, type Config, type Status} from './model.js';

    let {config, status, notices, onchanged}: {config: Config; status: Status | undefined; notices: NoticesStore; onchanged: (c: Config) => void} = $props();

    let dialogOpen = $state(false);
    let editing = $state<Channel | undefined>();
    let newType = $state<ChannelType | undefined>();
    let picking = $state(false);
    let removing = $state<Channel | undefined>();
    let testing = $state('');

    function add(type: ChannelType) {
        picking = false;
        editing = undefined;
        newType = type;
        dialogOpen = true;
    }

    function edit(ch: Channel) {
        editing = ch;
        newType = undefined;
        dialogOpen = true;
    }

    async function toggle(ch: Channel) {
        try {
            onchanged(await put<Config>(`api/v1/channels/${encodeURIComponent(ch.id)}`, channelBody({...ch, enabled: !ch.enabled})));
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    async function test(ch: Channel) {
        testing = ch.id;
        try {
            await post('api/v1/channels/test', channelBody(ch));
            notices.push('info', t('The test message to {name} went out.', {name: ch.name}));
        } catch (e) {
            const b = e instanceof ApiError ? (e.body as {step?: string; message?: string}) : undefined;
            notices.push('error', t('{name}: {error}', {name: ch.name, error: (b?.step ? t('step.' + b.step) + ': ' : '') + (b?.message ?? (e as Error).message)}));
        } finally {
            testing = '';
        }
    }

    async function remove() {
        const ch = removing;
        removing = undefined;
        if (!ch) return;
        try {
            onchanged(await del<Config>(`api/v1/channels/${encodeURIComponent(ch.id)}`));
            notices.push('info', t('Removed.'));
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    const usedBy = (id: string) => config.rules.filter((r) => r.channels.includes(id)).length;
</script>

<section class="nfy-section nfy-section-wide" data-testid="channels">
    <div class="nfy-section-head">
        <h2>{t('Channels')}</h2>
        <button class="hmm-button nfy-primary" onclick={() => (picking = !picking)} data-testid="channel-add">{t('Add channel')}</button>
    </div>
    {#if picking || config.channels.length === 0}
        <p class="nfy-help">{t('Where should messages go? Choose the kind of channel.')}</p>
        <div class="nfy-type-grid" data-testid="type-grid">
            {#each config.types as type (type)}
                <button class="hmm-button" onclick={() => add(type)} data-testid="type-{type}">{TYPE_NAMES[type]}</button>
            {/each}
        </div>
    {/if}
    {#if config.channels.length > 0}
        <table class="nfy-grid" data-testid="channel-list">
            <thead>
                <tr><th>{t('Name')}</th><th>{t('Kind')}</th><th>{t('Sends to')}</th><th>{t('State')}</th><th></th></tr>
            </thead>
            <tbody>
                {#each config.channels as ch (ch.id)}
                    {@const st = status?.channels[ch.id]}
                    <tr data-testid="channel-{ch.id}">
                        <td>{ch.name}</td>
                        <td>{TYPE_NAMES[ch.type]}</td>
                        <td class="nfy-code">{channelTarget(ch)}</td>
                        <td>
                            {#if !ch.enabled}
                                <span class="nfy-pill">{t('off')}</span>
                            {:else if st?.failing}
                                <span class="nfy-pill bad" title={st.last_error}>{t('failing')}</span>
                            {:else if st?.last_ok}
                                <span class="nfy-pill ok">{t('works')}</span>
                            {:else}
                                <span class="nfy-pill">{t('not used yet')}</span>
                            {/if}
                        </td>
                        <td class="nfy-cell-actions">
                            <button class="hmm-button nfy-fixed" disabled={testing !== ''} onclick={() => test(ch)} data-testid="channel-test-{ch.id}">{testing === ch.id ? t('Testing…') : t('Test')}</button>
                            <button class="hmm-button nfy-switch" onclick={() => toggle(ch)} data-testid="channel-toggle-{ch.id}">{ch.enabled ? t('Switch off') : t('Switch on')}</button>
                            <button class="hmm-button" onclick={() => edit(ch)} data-testid="channel-edit-{ch.id}">{t('Edit')}</button>
                            <button class="hmm-button" onclick={() => (removing = ch)} data-testid="channel-remove-{ch.id}">{t('Remove')}</button>
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
    <p class="nfy-help">{t('A channel receives what the rules send to it. The rules that are there from the start send to every channel.')}</p>
</section>

<ChannelDialog open={dialogOpen} channel={editing} type={newType} onclose={() => (dialogOpen = false)} onsaved={(c) => ((dialogOpen = false), onchanged(c), notices.push('info', t('Saved.')))} />

<ConfirmDialog
    open={removing !== undefined}
    title={t('Remove channel')}
    message={removing
        ? t('Remove the channel {name}? Its credentials are deleted.', {name: removing.name}) + (usedBy(removing.id) ? ' ' + t('{n} rules name it; a rule that sends only there is switched off.', {n: usedBy(removing.id)}) : '')
        : ''}
    confirmLabel={t('Remove')}
    danger
    onconfirm={remove}
    onclose={() => (removing = undefined)}
/>
