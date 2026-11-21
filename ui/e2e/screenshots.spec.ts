import {expect, test, type APIRequestContext} from '@playwright/test';

// The readme's screenshots (SCREENSHOTS=1 npx playwright test screenshots): German, light and
// dark, against the daemon on the fake system. The channels send into the fake's sinks.
test.skip(!process.env.SCREENSHOTS, 'only on demand');
test.use({locale: 'de-DE', viewport: {width: 1280, height: 860}});

const FAKE = 'http://127.0.0.1:18080';
const API = 'http://127.0.0.1:18300/addons/notify/api/v1/';
const headers = {'X-CCU-Addon-Request': '1', Origin: 'http://127.0.0.1:18300'};

async function setup(request: APIRequestContext) {
    const cfg = await (await request.get(API + 'config')).json();
    if (cfg.channels.length > 0) return;
    const post = async (path: string, data: unknown) => expect((await request.post(API + path, {headers, data})).ok()).toBeTruthy();
    const put = async (path: string, data: unknown) => expect((await request.put(API + path, {headers, data})).ok()).toBeTruthy();
    await post('channels', {name: 'Handy (Pushover)', type: 'pushover', enabled: true, token: 'app', user_key: 'user', target: 'pixel', url: FAKE + '/sink/pushover'});
    await post('channels', {name: 'Familie (Telegram)', type: 'telegram', enabled: true, token: '123:abc', target: '-1001234567890', url: FAKE + '/sink/telegram'});
    await post('channels', {name: 'E-Mail', type: 'email', enabled: true, host: 'smtp.example.org', security: 'starttls', user: 'ccu@example.org', password: 'x', sender: 'CCU <ccu@example.org>', target: 'ich@example.org'});
    await post('channels', {name: 'MQTT', type: 'mqtt', enabled: false});
    for (const r of cfg.rules as {id: string; source: string; channels: string[]}[]) {
        const channels = r.source === 'digest' ? ['e-mail'] : ['handy-pushover', 'familie-telegram'];
        await put('rules/' + r.id, {...r, channels, enabled: true, batch_s: r.source === 'digest' ? 0 : 1, hold_s: 0});
    }
    await post('rules', {name: 'Fenster Küche länger offen', source: 'datapoint', enabled: true, channels: ['handy-pushover'], priority: 'normal', resolved: true, batch_s: 0,
        datapoint: {interface: 'HmIP-RF', address: '0000D3C99C6B41:1', key: 'STATE', op: 'eq', value: '1', for_s: 0, label: 'länger als 10 Minuten offen'}});
    await put('settings', {language: 'de', system_name: 'CCU Keller', base_url: 'https://ccu.example.org', title_prefix: false, quiet: {enabled: true, from: '22:00', to: '07:00'}, repeat_min: 60, rate_per_hour: 30, inbound: {enabled: true}});
    // what happens after the start is announced
    await request.post(FAKE + '/dev/messages', {data: []});
    await new Promise((r) => setTimeout(r, 1500));
    await request.post(FAKE + '/dev/warnings', {data: [
        {id: 'backup-unencrypted', variant: 'plain', severity: 'warning'},
        {id: 'addon-failed', variant: 'mosquitto', severity: 'error', params: {addons: [{id: 'mosquitto', name: 'Mosquitto'}]}, href: '/addons'},
    ]});
    const msg = (address: string, key: string, name: string, room: string) => ({interface: 'HmIP-RF', address, channel: '0', key, value: true, since: new Date().toISOString(), seen: 'event', name, enums: ['room/x'], enum_names: [room]});
    await request.post(FAKE + '/dev/messages', {data: [msg('000A1BE9A1B2C3', 'UNREACH', 'Heizung Bad', 'Bad'), msg('0000D3C99C6B41', 'LOW_BAT', 'Fenster Küche', 'Küche')]});
    await request.post(FAKE + '/dev/event', {data: {interface: 'HmIP-RF', address: '0000D3C99C6B41:1', key: 'STATE', value: 1}});
    // a change of the configuration makes the daemon read the warnings at once
    await put('rules/digest', {...cfg.rules.find((r: {id: string}) => r.id === 'digest'), channels: ['e-mail'], enabled: true});
    await new Promise((r) => setTimeout(r, 4000));
}

for (const scheme of ['light', 'dark'] as const) {
    test(`screenshots ${scheme}`, async ({page, request}) => {
        test.setTimeout(90_000);
        await setup(request);
        await page.emulateMedia({colorScheme: scheme});
        const suffix = scheme === 'dark' ? '-dunkel' : '';
        const out = (n: string) => `../docs/screenshots/${n}${suffix}.png`;
        await page.goto('./');
        await expect(page.getByTestId('chstat-handy-pushover')).toContainText('funktioniert', {timeout: 20_000});
        await page.waitForTimeout(500);
        await page.screenshot({path: out('01-status')});
        await page.getByRole('tab', {name: /Kanäle/}).click();
        await page.waitForTimeout(300);
        await page.screenshot({path: out('02-kanaele')});
        await page.getByTestId('channel-edit-e-mail').click();
        await page.getByTestId('ch-port').fill('45997');
        await page.getByTestId('ch-host').fill('127.0.0.1');
        await page.getByTestId('ch-test').click();
        await expect(page.getByTestId('ch-result')).toContainText('Verbindung', {timeout: 20_000});
        await page.screenshot({path: out('03-kanal-test')});
        await page.keyboard.press('Escape');
        await page.getByRole('tab', {name: /Regeln/}).click();
        await page.waitForTimeout(300);
        await page.screenshot({path: out('04-regeln')});
        await page.getByTestId('rule-edit-fenster-kueche-laenger-offen').click();
        await expect(page.getByTestId('r-preview-title')).toBeVisible();
        await page.waitForTimeout(500);
        await page.screenshot({path: out('05-regel')});
        await page.keyboard.press('Escape');
        await page.getByRole('tab', {name: /Verlauf/}).click();
        await expect(page.getByTestId('history-row').first()).toBeVisible();
        await page.waitForTimeout(300);
        await page.screenshot({path: out('06-verlauf')});
        await page.getByRole('tab', {name: /Einstellungen/}).click();
        await page.waitForTimeout(500);
        await page.screenshot({path: out('07-einstellungen'), fullPage: true});
    });
}
