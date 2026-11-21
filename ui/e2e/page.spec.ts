import {expect, test, type Page} from '@playwright/test';

// One run through the page against the daemon on the fake system: a channel with its Test
// button (a refusing target, then a working one), the rules with the preview, a warning and a
// datapoint rule end to end into the fake's sink, the history, the settings with the inbound
// webhook. The steps depend on each other (one daemon, one configuration).
test.describe.configure({mode: 'serial'});

const FAKE = 'http://127.0.0.1:18080';
const DAEMON = 'http://127.0.0.1:18300';

interface Sunk {
    path: string;
    body: string;
    header: Record<string, string>;
}

async function sink(page: Page): Promise<Sunk[]> {
    const res = await page.request.get(`${FAKE}/dev/sink`);
    return (await res.json()) as Sunk[];
}

async function titles(page: Page, path: string): Promise<string[]> {
    return (await sink(page)).filter((r) => r.path === path).map((r) => (JSON.parse(r.body) as {title: string}).title);
}

async function tab(page: Page, name: RegExp) {
    await page.getByRole('tab', {name}).click();
}

test('starts without a channel and says so', async ({page}) => {
    await page.goto('./');
    await expect(page.getByTestId('no-channel')).toBeVisible();
    await expect(page.getByTestId('source-warnings')).toContainText('1 on the Status page');
    await expect(page.getByTestId('source-datapoints')).toContainText('no rule');
    // what was there at the start is listed, not announced
    await expect(page.getByTestId('open')).toContainText('Die Sicherung ist nicht verschlüsselt');
    await page.getByTestId('go-channels').click();
    await expect(page.getByTestId('type-grid')).toBeVisible();
});

test('adds a webhook channel after its test said what was wrong', async ({page}) => {
    await page.goto('./');
    await tab(page, /Channels/);
    await page.getByTestId('type-webhook').click();
    await expect(page.getByTestId('ch-save')).toBeDisabled();
    await page.getByTestId('ch-name').fill('Sink');
    await page.getByTestId('ch-url').fill('http://127.0.0.1:45999/nothing');
    await page.getByTestId('ch-test').click();
    await expect(page.getByTestId('ch-result')).toContainText('connection: ');
    await expect(page.getByTestId('ch-result')).toContainText('refused');
    // a target that answers 401
    await page.request.post(`${FAKE}/dev/sink-status`, {data: {refuse: 401}});
    await page.getByTestId('ch-url').fill(`${FAKE}/sink/refuse`);
    await page.getByTestId('ch-test').click();
    await expect(page.getByTestId('ch-result')).toContainText('answer: the webhook answered 401');
    await page.getByTestId('ch-url').fill(`${FAKE}/sink/hook`);
    await page.getByTestId('ch-header-add').click();
    await page.getByTestId('ch-header-name-0').fill('X-Key');
    await page.getByTestId('ch-header-value-0').fill('header-secret');
    await page.getByTestId('ch-test').click();
    await expect(page.getByTestId('ch-result')).toContainText('The test message went out');
    const got = (await sink(page)).filter((r) => r.path === '/sink/hook');
    expect(got).toHaveLength(1);
    expect(got[0]!.header['X-Key']).toBe('header-secret');
    expect(JSON.parse(got[0]!.body).title).toBe('Testnachricht');
    await page.getByTestId('ch-save').click();
    await expect(page.getByTestId('channel-sink')).toContainText(`${FAKE}/sink/hook`);
    // the secret is not in the page again
    await page.getByTestId('channel-edit-sink').click();
    await expect(page.getByTestId('ch-header-value-0')).toHaveValue('');
    await expect(page.getByTestId('ch-header-value-0')).toHaveAttribute('placeholder', 'unchanged');
    // and the test of the saved channel uses the stored one
    await page.getByTestId('ch-test').click();
    await expect(page.getByTestId('ch-result')).toContainText('The test message went out');
    expect((await sink(page)).filter((r) => r.path === '/sink/hook')[1]!.header['X-Key']).toBe('header-secret');
});

test('an email channel names its defaults and needs its fields', async ({page}) => {
    await page.goto('./');
    await tab(page, /Channels/);
    await page.getByTestId('channel-add').click();
    await page.getByTestId('type-email').click();
    await expect(page.getByTestId('ch-port')).toHaveAttribute('placeholder', '587');
    await page.getByTestId('ch-security').selectOption('tls');
    await expect(page.getByTestId('ch-port')).toHaveAttribute('placeholder', '465');
    await expect(page.getByTestId('ch-test')).toBeDisabled();
    await page.getByTestId('ch-host').fill('127.0.0.1');
    await page.getByTestId('ch-port').fill('45998');
    await page.getByTestId('ch-sender').fill('ccu@example.org');
    await page.getByTestId('ch-target').fill('me@example.org');
    await page.getByTestId('ch-test').click();
    await expect(page.getByTestId('ch-result')).toContainText('connection: 127.0.0.1:45998');
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('channel-list')).not.toContainText('E-Mail');
});

test('the rules are there, and a rule shows what it would send', async ({page}) => {
    await page.goto('./');
    await tab(page, /Rules/);
    await expect(page.getByTestId('rule-warnings')).toContainText('every channel');
    await expect(page.getByTestId('rule-service')).toContainText('after 2 min');
    await expect(page.getByTestId('rule-digest')).toContainText('daily at 08:00');
    // the warnings rule: at once instead of batched, with a title of its own
    await page.getByTestId('rule-edit-warnings').click();
    await expect(page.getByTestId('r-preview-title')).toContainText('Sicherungsziel');
    await page.getByTestId('r-batch').fill('0');
    await page.getByTestId('r-templates').check();
    await page.getByTestId('r-title').fill('[{{ system }}] {{ headline }');
    await expect(page.getByTestId('r-preview-error')).toBeVisible();
    await page.getByTestId('r-title').fill('[CCU] {{ headline }}');
    await expect(page.getByTestId('r-preview-title')).toHaveText('[CCU] Das Sicherungsziel USB funktioniert nicht (no-medium)');
    await page.getByTestId('r-save').click();
    await expect(page.getByTestId('rule-warnings')).toContainText('at once');
    // its Test sends the sample to the channel
    await page.getByTestId('rule-test-warnings').click();
    await expect.poll(async () => titles(page, '/sink/hook')).toContain('[CCU] Das Sicherungsziel USB funktioniert nicht (no-medium)');
});

test('a new warning arrives, and its end', async ({page}) => {
    await page.goto('./');
    await page.request.post(`${FAKE}/dev/warnings`, {
        data: [
            {id: 'backup-unencrypted', variant: 'plain', severity: 'warning'},
            {id: 'addon-failed', variant: 'mosquitto', severity: 'error', params: {addons: [{id: 'mosquitto', name: 'Mosquitto'}]}, href: '/addons'},
        ],
    });
    // a change of the configuration makes the daemon read at once (it polls every 30 s)
    await tab(page, /Rules/);
    await page.getByTestId('rule-toggle-digest').click();
    await expect.poll(async () => titles(page, '/sink/hook'), {timeout: 15_000}).toContain('[CCU] Addon fehlgeschlagen: Mosquitto');
    await page.request.post(`${FAKE}/dev/warnings`, {data: [{id: 'backup-unencrypted', variant: 'plain', severity: 'warning'}]});
    await page.getByTestId('rule-toggle-digest').click();
    await expect.poll(async () => titles(page, '/sink/hook'), {timeout: 15_000}).toContain('[CCU] Behoben: Addon fehlgeschlagen: Mosquitto');
    // the one that was there from the start was never announced
    expect((await titles(page, '/sink/hook')).some((t) => t.includes('nicht verschlüsselt'))).toBe(false);
});

test('a datapoint rule picked from the device list fires and resolves', async ({page}) => {
    await page.goto('./');
    await tab(page, /Rules/);
    await page.getByTestId('rule-add').click();
    await page.getByTestId('source-datapoint').click();
    await expect(page.getByTestId('r-save')).toBeDisabled();
    await page.getByTestId('r-name').fill('Fenster Küche');
    await page.getByTestId('r-device').selectOption({label: 'Fenster Küche (Küche) · 0000D3C99C6B41:1'});
    await expect(page.getByTestId('r-address')).toHaveValue('0000D3C99C6B41:1');
    await expect(page.getByTestId('r-key')).toHaveValue('STATE');
    await page.getByTestId('r-op').selectOption('eq');
    await page.getByTestId('r-value').fill('1');
    await page.getByTestId('r-label').fill('offen');
    await expect(page.getByTestId('r-preview-title')).toHaveText('Fenster Küche: offen');
    await page.getByTestId('r-save').click();
    await expect(page.getByTestId('rule-fenster-kueche')).toContainText('0000D3C99C6B41:1 STATE equals 1');
    await tab(page, /Status/);
    await expect(page.getByTestId('source-datapoints')).toContainText('1 datapoints watched');
    await page.request.post(`${FAKE}/dev/event`, {data: {interface: 'HmIP-RF', address: '0000D3C99C6B41:1', key: 'STATE', value: 1}});
    await expect.poll(async () => titles(page, '/sink/hook'), {timeout: 15_000}).toContain('Fenster Küche: offen');
    await expect(page.getByTestId('open')).toContainText('Fenster Küche: offen');
    await page.request.post(`${FAKE}/dev/event`, {data: {interface: 'HmIP-RF', address: '0000D3C99C6B41:1', key: 'STATE', value: 0}});
    await expect.poll(async () => titles(page, '/sink/hook'), {timeout: 15_000}).toContain('Behoben: Fenster Küche: offen');
});

test('the history lists what went out, the status how the channel does', async ({page}) => {
    await page.goto('./');
    await expect(page.getByTestId('chstat-sink')).toContainText('works');
    await tab(page, /History/);
    await expect(page.getByTestId('history-row').first()).toContainText('Behoben: Fenster Küche: offen');
    await expect(page.getByTestId('history')).toContainText('sent');
    // a target that fails: the history says why, the status shows it
    await page.request.post(`${FAKE}/dev/sink-status`, {data: {hook: 403}});
    await tab(page, /Rules/);
    await page.getByTestId('rule-test-system').click();
    await tab(page, /History/);
    await page.getByTestId('only-failed').check();
    await expect(page.getByTestId('history-row').first()).toContainText('answer: the webhook answered 403', {timeout: 15_000});
    await expect(page.getByTestId('history-row').first()).toContainText('failed');
    await tab(page, /Status/);
    await expect(page.getByTestId('chstat-sink')).toContainText('failing');
    await page.request.post(`${FAKE}/dev/sink-status`, {data: {hook: 200}});
    await tab(page, /Channels/);
    await page.getByTestId('channel-test-sink').click();
    await tab(page, /History/);
    await page.getByTestId('history-clear').click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('history-empty')).toBeVisible();
});

test('the settings: quiet hours, and the inbound webhook end to end', async ({page}) => {
    await page.goto('./');
    await tab(page, /Settings/);
    await expect(page.getByTestId('settings-save')).toBeDisabled();
    // off: the address answers 404
    const secretField = page.getByTestId('inbound-secret');
    await expect(secretField).toHaveAttribute('type', 'password');
    await page.getByTestId('inbound-show').click();
    const secret = await secretField.inputValue();
    expect(secret).toMatch(/^[0-9a-f]{32}$/);
    await expect(page.getByTestId('inbound-url')).toHaveText(`${DAEMON}/inbound`);
    expect((await page.request.post(`${DAEMON}/inbound`, {headers: {Authorization: `Bearer ${secret}`}, data: {title: 'x'}})).status()).toBe(404);
    await page.getByTestId('s-inbound').check();
    await page.getByTestId('s-language').selectOption('en');
    await page.getByTestId('s-system-name').fill('Lab CCU');
    await page.getByTestId('s-title-prefix').check();
    await page.getByTestId('settings-save').click();
    await expect(page.getByTestId('settings-save')).toBeDisabled();
    // a rule for what comes in
    await tab(page, /Rules/);
    await page.getByTestId('rule-add').click();
    await page.getByTestId('source-inbound').click();
    await page.getByTestId('r-save').click();
    await expect(page.getByTestId('rule-inbound-webhook')).toContainText('every sender');
    expect((await page.request.post(`${DAEMON}/inbound`, {data: {title: 'x'}})).status()).toBe(401);
    const res = await page.request.post(`${DAEMON}/inbound`, {headers: {Authorization: `Bearer ${secret}`}, data: {title: 'Washing machine', text: 'done', source: 'house'}});
    expect(res.status()).toBe(202);
    await expect.poll(async () => titles(page, '/sink/hook'), {timeout: 15_000}).toContain('Lab CCU: Washing machine');
    // a new secret ends the old one
    await tab(page, /Settings/);
    await page.getByTestId('inbound-rotate').click();
    await page.getByTestId('confirm-yes').click();
    await expect(secretField).not.toHaveValue(secret);
    expect((await page.request.post(`${DAEMON}/inbound`, {headers: {Authorization: `Bearer ${secret}`}, data: {title: 'x'}})).status()).toBe(401);
    // quiet hours around the clock: the status says so
    await page.getByTestId('s-quiet').check();
    await page.getByTestId('s-quiet-from').fill('00:00');
    await page.getByTestId('s-quiet-to').fill('23:59');
    await page.getByTestId('settings-save').click();
    await tab(page, /Status/);
    await expect(page.getByTestId('quiet')).toBeVisible();
});

test('removing the channel asks first and switches nothing on by itself', async ({page}) => {
    await page.goto('./');
    await tab(page, /Channels/);
    await page.getByTestId('channel-remove-sink').click();
    await expect(page.getByTestId('confirm')).toContainText('Its credentials are deleted');
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('type-grid')).toBeVisible();
    await tab(page, /Status/);
    await expect(page.getByTestId('no-channel')).toBeVisible();
});

test('German and dark: the page follows the browser when it is not in the shell', async ({browser}) => {
    const context = await browser.newContext({locale: 'de-DE', colorScheme: 'dark', baseURL: 'http://127.0.0.1:18300/addons/notify/'});
    const page = await context.newPage();
    await page.goto('./');
    await expect(page.getByRole('tab', {name: /Kanäle/})).toBeVisible();
    await expect(page.getByRole('heading', {name: 'Benachrichtigungen'})).toBeVisible();
    await expect(page.getByTestId('source-warnings')).toContainText('Warnungen der Status-Seite');
    // dark: the page's background is dark, its text light
    const [bg, fg] = await page.evaluate(() => {
        const lum = (c: string) => {
            const m = c.match(/[\d.]+/g)!.map(Number);
            return 0.2126 * m[0]! + 0.7152 * m[1]! + 0.0722 * m[2]!;
        };
        const cs = getComputedStyle(document.body);
        return [lum(cs.backgroundColor), lum(cs.color)];
    });
    expect(bg).toBeLessThan(80);
    expect(fg).toBeGreaterThan(150);
    // a pill keeps its box whatever it says
    const widths = await page.locator('.nfy-pill').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().width)));
    expect(widths.length).toBeGreaterThan(2);
    expect(new Set(widths).size).toBe(1);
    await context.close();
});
