<script lang="ts">
    // What the addon sees and how its channels do: the sources, what is open, the channels'
    // last success and last failure.
    import {i18n, t} from 'occulite-ui/i18n';

    import {clock, TYPE_NAMES, type Config, type Item, type SourceState, type Status} from './model.js';

    let {config, status, onopen}: {config: Config; status: Status | undefined; onopen: (tab: string) => void} = $props();

    const SOURCE_ROWS = ['warnings', 'service', 'system', 'datapoints'];

    function sourceState(name: string, s: SourceState | undefined): {cls: string; text: string} {
        if (!s || !s.at) return {cls: '', text: t('starting')};
        if (!s.ok) return {cls: 'bad', text: t('error')};
        if (name === 'datapoints' && s.watching === 0) return {cls: '', text: t('no rule')};
        return {cls: 'ok', text: t('reading')};
    }

    function sourceDetail(name: string, s: SourceState | undefined): string {
        if (!s) return '';
        if (s.error) return s.error;
        switch (name) {
            case 'warnings':
                return t('{n} on the Status page', {n: s.open});
            case 'service':
                return t('{n} active', {n: s.open}) + ' · ' + (s.stream ? t('live stream') : t('polled every minute'));
            case 'datapoints':
                return s.watching ? t('{n} datapoints watched', {n: s.watching}) + (s.stream ? ' · ' + t('live stream') : '') : t('The event stream is opened only while a datapoint rule exists.');
            default:
                return t('version, backups, restarts');
        }
    }

    const groups = $derived<{title: string; items: Item[]}[]>(
        status
            ? [
                  {title: t('Warnings'), items: status.open.warnings},
                  {title: t('Service messages'), items: status.open.service},
                  {title: t('Datapoint rules that hold'), items: status.open.datapoints},
              ]
            : [],
    );
    const openCount = $derived(groups.reduce((n, g) => n + g.items.length, 0));
    const enabled = $derived(config.channels.filter((c) => c.enabled));
</script>

{#if config.channels.length === 0}
    <section class="nfy-section">
        <p class="nfy-notice" data-testid="no-channel">
            {t('No channel yet: nothing is sent. Add one, and the warnings, the service messages and the system events go there at once.')}
            <button class="hmm-button nfy-primary" onclick={() => onopen('channels')} data-testid="go-channels">{t('Add a channel')}</button>
        </p>
    </section>
{/if}

<section class="nfy-section" data-testid="sources">
    <h2>{t('What the addon reads')}</h2>
    <table class="nfy-grid">
        <tbody>
            {#each SOURCE_ROWS as name (name)}
                {@const s = status?.sources[name]}
                {@const st = sourceState(name, s)}
                <tr data-testid="source-{name}">
                    <td class="nfy-nowrap">{t('source.' + name)}</td>
                    <td><span class={'nfy-pill ' + st.cls} title={st.text}>{st.text}</span></td>
                    <td class={s?.error ? 'nfy-error-inline' : 'nfy-muted'}>{sourceDetail(name, s)}</td>
                </tr>
            {/each}
        </tbody>
    </table>
    {#if status?.quiet_until}
        <p class="nfy-notice" data-testid="quiet">{t('Quiet hours until {time}: rules that respect them hold their messages back.', {time: new Date(status.quiet_until).toLocaleTimeString(i18n.language === 'de' ? 'de-DE' : 'en-GB', {hour: '2-digit', minute: '2-digit'})})}</p>
    {/if}
    {#if status && status.pending > 0}
        <p class="nfy-muted" data-testid="pending">{t('{n} events wait in the batches.', {n: status.pending})}</p>
    {/if}
</section>

<section class="nfy-section" data-testid="channel-status">
    <h2>{t('Channels')}</h2>
    {#if enabled.length === 0}
        <p class="nfy-muted">{t('No enabled channel.')}</p>
    {:else}
        <table class="nfy-grid">
            <thead>
                <tr><th>{t('Name')}</th><th>{t('State')}</th><th>{t('Sent')}</th><th>{t('Failed')}</th><th>{t('Last success')}</th><th>{t('Last failure')}</th></tr>
            </thead>
            <tbody>
                {#each enabled as ch (ch.id)}
                    {@const st = status?.channels[ch.id]}
                    <tr data-testid="chstat-{ch.id}">
                        <td>{ch.name} <span class="nfy-faint">{TYPE_NAMES[ch.type]}</span></td>
                        <td>
                            {#if st?.failing}
                                <span class="nfy-pill bad">{t('failing')}</span>
                            {:else if st?.last_ok}
                                <span class="nfy-pill ok">{t('works')}</span>
                            {:else}
                                <span class="nfy-pill">{t('not used yet')}</span>
                            {/if}
                        </td>
                        <td>{st?.sent ?? 0}{st?.queued ? ' (+' + st.queued + ')' : ''}</td>
                        <td>{st?.failed ?? 0}</td>
                        <td class="nfy-nowrap">{clock(st?.last_ok, i18n.language)}</td>
                        <td class={st?.failing ? 'nfy-error-inline' : 'nfy-muted'}>
                            {#if st?.last_error}{clock(st.last_error_at, i18n.language)}: {st.last_step ? t('step.' + st.last_step) + ': ' : ''}{st.last_error}{/if}
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
</section>

<section class="nfy-section" data-testid="open">
    <h2>{t('Open right now')}</h2>
    {#if !status}
        <p class="nfy-muted">{t('Loading…')}</p>
    {:else if openCount === 0}
        <p class="nfy-ok" data-testid="all-clear">{t('All clear: no warning, no service message.')}</p>
    {:else}
        {#each groups as g (g.title)}
            {#if g.items.length}
                <h3>{g.title} ({g.items.length})</h3>
                <ul class="nfy-list">
                    {#each g.items as it (it.key)}
                        <li>{it.line}</li>
                    {/each}
                </ul>
            {/if}
        {/each}
    {/if}
    <p class="nfy-help">{t('What was already there when the addon started is listed here, but only what changes afterwards is announced.')}</p>
</section>
