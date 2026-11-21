<script lang="ts">
    // The rules: which events go to which channels, and how.
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import ConfirmDialog from './ConfirmDialog.svelte';
    import RuleDialog from './RuleDialog.svelte';
    import {del, post, put} from './api.js';
    import {ruleChannels, ruleFilter, seconds, SOURCES, type Config, type Rule, type Source} from './model.js';

    let {config, notices, onchanged}: {config: Config; notices: NoticesStore; onchanged: (c: Config) => void} = $props();

    let dialogOpen = $state(false);
    let editing = $state<Rule | undefined>();
    let newSource = $state<Source | undefined>();
    let picking = $state(false);
    let removing = $state<Rule | undefined>();
    let testing = $state('');

    function add(source: Source) {
        picking = false;
        editing = undefined;
        newSource = source;
        dialogOpen = true;
    }

    async function toggle(r: Rule) {
        try {
            onchanged(await put<Config>(`api/v1/rules/${encodeURIComponent(r.id)}`, {...r, enabled: !r.enabled}));
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    async function test(r: Rule) {
        testing = r.id;
        try {
            const out = await post<{channels: string[]}>(`api/v1/rules/${encodeURIComponent(r.id)}/test`);
            notices.push('info', t('A sample of this rule went to {n} channels: see History.', {n: out.channels.length}));
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            testing = '';
        }
    }

    async function remove() {
        const r = removing;
        removing = undefined;
        if (!r) return;
        try {
            onchanged(await del<Config>(`api/v1/rules/${encodeURIComponent(r.id)}`));
            notices.push('info', t('Removed.'));
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }

    function delivery(r: Rule): string {
        const parts = [t('priority.' + r.priority)];
        if (r.source !== 'digest') {
            parts.push(r.batch_s ? t('batched {time}', {time: seconds(r.batch_s)}) : t('at once'));
            if (r.resolved) parts.push(t('also when over'));
            if (r.ignore_quiet) parts.push(t('ignores quiet hours'));
        }
        return parts.join(' · ');
    }
</script>

<section class="nfy-section nfy-section-wide" data-testid="rules">
    <div class="nfy-section-head">
        <h2>{t('Rules')}</h2>
        <button class="hmm-button nfy-primary" onclick={() => (picking = !picking)} data-testid="rule-add">{t('Add rule')}</button>
    </div>
    {#if picking}
        <p class="nfy-help">{t('What should the rule report?')}</p>
        <div class="nfy-type-grid" data-testid="source-grid">
            {#each SOURCES as s (s)}
                <button class="hmm-button" onclick={() => add(s)} data-testid="source-{s}">{t('rule.' + s)}</button>
            {/each}
        </div>
    {/if}
    {#if config.rules.length === 0}
        <p class="nfy-muted">{t('No rules: nothing is sent.')}</p>
    {:else}
        <table class="nfy-grid" data-testid="rule-list">
            <thead>
                <tr><th>{t('Name')}</th><th>{t('Reports')}</th><th>{t('Filter')}</th><th>{t('To')}</th><th>{t('Delivery')}</th><th></th></tr>
            </thead>
            <tbody>
                {#each config.rules as r (r.id)}
                    <tr data-testid="rule-{r.id}" class={r.enabled ? '' : 'nfy-off'}>
                        <td>{r.name}</td>
                        <td class="nfy-nowrap">{t('rule.' + r.source)}</td>
                        <td>{ruleFilter(r, t)}</td>
                        <td>{ruleChannels(r, config.channels, t)}</td>
                        <td>{delivery(r)}</td>
                        <td class="nfy-cell-actions">
                            <button class="hmm-button nfy-fixed" disabled={testing !== '' || config.channels.length === 0} onclick={() => test(r)} data-testid="rule-test-{r.id}">{testing === r.id ? t('Testing…') : t('Test')}</button>
                            <button class="hmm-button nfy-switch" onclick={() => toggle(r)} data-testid="rule-toggle-{r.id}">{r.enabled ? t('Switch off') : t('Switch on')}</button>
                            <button class="hmm-button" onclick={() => ((editing = r), (newSource = undefined), (dialogOpen = true))} data-testid="rule-edit-{r.id}">{t('Edit')}</button>
                            <button class="hmm-button" onclick={() => (removing = r)} data-testid="rule-remove-{r.id}">{t('Remove')}</button>
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
    <p class="nfy-help">{t('Every event runs through all rules of its kind; each rule that takes it sends its own message. Test sends a sample of the rule to its channels.')}</p>
</section>

<RuleDialog open={dialogOpen} rule={editing} source={newSource} {config} onclose={() => (dialogOpen = false)} onsaved={(c) => ((dialogOpen = false), onchanged(c), notices.push('info', t('Saved.')))} />

<ConfirmDialog open={removing !== undefined} title={t('Remove rule')} message={removing ? t('Remove the rule {name}?', {name: removing.name}) : ''} confirmLabel={t('Remove')} danger onconfirm={remove} onclose={() => (removing = undefined)} />
