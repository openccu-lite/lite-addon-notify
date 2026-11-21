<script lang="ts">
    // The page: what is open and how the channels do (Status), the channels, the rules, the
    // history of deliveries, the settings.
    import {onMount} from 'svelte';
    import {Notices, Tabs, ThemeSwitch} from 'occulite-ui';
    import {i18n, t} from 'occulite-ui/i18n';
    import {NoticesStore} from 'occulite-ui/notices';
    import {cycleTheme, theme} from 'occulite-ui/theme';

    import ChannelsTab from './ChannelsTab.svelte';
    import HistoryTab from './HistoryTab.svelte';
    import RulesTab from './RulesTab.svelte';
    import SettingsTab from './SettingsTab.svelte';
    import StatusTab from './StatusTab.svelte';
    import {get} from './api.js';
    import type {Config, Status} from './model.js';

    const notices = new NoticesStore();
    let tab = $state('status');
    let config = $state<Config | undefined>();
    let status = $state<Status | undefined>();
    let loadError = $state('');

    async function loadConfig() {
        try {
            config = await get<Config>('api/v1/config');
            loadError = '';
        } catch (e) {
            loadError = (e as Error).message;
        }
    }

    async function loadStatus() {
        try {
            status = await get<Status>('api/v1/status');
        } catch {
            /* the next poll tries again; the configuration's error is the one shown */
        }
    }

    onMount(() => {
        void loadConfig();
        void loadStatus();
        const poll = setInterval(() => void loadStatus(), 3000);
        return () => clearInterval(poll);
    });

    function changed(c: Config) {
        config = c;
        void loadStatus();
    }

    const failing = $derived(config ? config.channels.filter((c) => c.enabled && status?.channels[c.id]?.failing).length : 0);
    const tabs = $derived([
        {id: 'status', label: t('Status'), ...(failing ? {badge: '!'} : {})},
        {id: 'channels', label: t('Channels'), ...(config && config.channels.length === 0 ? {badge: '!'} : {})},
        {id: 'rules', label: t('Rules')},
        {id: 'history', label: t('History')},
        {id: 'settings', label: t('Settings')},
    ]);
</script>

<div class="nfy-app" lang={i18n.language}>
    <header class="nfy-header">
        <h1>{t('Notifications')}</h1>
        <Tabs {tabs} bind:active={tab} label={t('Notifications')} testId="tabs" />
        <span class="nfy-spacer"></span>
        {#if config}<span class="nfy-muted nfy-small">{t('Version {v}', {v: config.version})}</span>{/if}
        {#if !theme.followsShell}
            <ThemeSwitch theme={theme.choice} onclick={cycleTheme} />
        {/if}
    </header>
    {#if loadError}
        <div class="nfy-banner" role="alert">{t('The page could not read the configuration: {error}', {error: loadError})}</div>
    {/if}
    <main class="nfy-page">
        {#if config}
            {#if tab === 'status'}
                <StatusTab {config} {status} onopen={(id) => (tab = id)} />
            {:else if tab === 'channels'}
                <ChannelsTab {config} {status} {notices} onchanged={changed} />
            {:else if tab === 'rules'}
                <RulesTab {config} {notices} onchanged={changed} />
            {:else if tab === 'history'}
                <HistoryTab {notices} />
            {:else}
                <SettingsTab {config} {notices} onchanged={changed} />
            {/if}
        {:else if !loadError}
            <p class="nfy-muted">{t('Loading…')}</p>
        {/if}
    </main>
    <Notices notices={notices.visible} ondismiss={(id) => notices.dismiss(id)} />
</div>
