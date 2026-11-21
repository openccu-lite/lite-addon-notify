import {describe, expect, it} from 'vitest';

import {channelBody, channelComplete, channelTarget, defaultPort, emptyChannel, emptyRule, FIELDS, headerRows, headersOf, ruleChannels, ruleFilter, seconds, SOURCES, type Channel, type ChannelType} from './model.js';

const t = (key: string, params?: Record<string, string | number>) => key.replace(/\{(\w+)\}/g, (_, n: string) => String(params?.[n] ?? ''));

describe('channels', () => {
    it('knows when a form is complete', () => {
        const ch = emptyChannel('pushover');
        ch.name = 'Push';
        expect(channelComplete(ch)).toBe(false);
        ch.token = 't';
        ch.user_key = 'u';
        expect(channelComplete(ch)).toBe(true);
        // a saved channel's secrets count although the API never answers them
        const saved: Channel = {...emptyChannel('pushover'), id: 'push', name: 'Push', secrets: {token: true, user_key: true}};
        expect(channelComplete(saved)).toBe(true);
        expect(channelComplete({...saved, clear: ['token']})).toBe(false);
        expect(channelComplete({...saved, name: ' '})).toBe(false);
        // a required field that is no secret must be in the form
        expect(channelComplete({...emptyChannel('telegram'), name: 'T', secrets: {token: true}})).toBe(false);
    });
    it('has a form for every type, with the secrets marked', () => {
        for (const [type, fields] of Object.entries(FIELDS)) {
            expect(fields.length, type).toBeGreaterThan(0);
            for (const f of fields) if (f.name === 'token' || f.name === 'password' || f.name === 'user_key') expect(f.kind, `${type}.${f.name}`).toBe('secret');
        }
        for (const type of ['discord', 'slack', 'matrix'] as ChannelType[]) expect(FIELDS[type][0]!.kind).toBe('secret');
    });
    it('names the default port and the target without a secret', () => {
        expect(defaultPort({...emptyChannel('email'), security: 'tls'})).toBe(465);
        expect(defaultPort(emptyChannel('email'))).toBe(587);
        expect(defaultPort(emptyChannel('mqtt'))).toBe(1883);
        expect(channelTarget({...emptyChannel('ntfy'), target: 'ccu'})).toBe('https://ntfy.sh/ccu');
        expect(channelTarget({...emptyChannel('mqtt'), target: 'notify/x'})).toBe('127.0.0.1:1883 · notify/x');
        expect(channelTarget({...emptyChannel('discord'), url: 'https://secret'})).toBe('');
    });
    it('builds the body a save sends', () => {
        const b = channelBody({...emptyChannel('mqtt'), name: 'M', port: '' as unknown as number, qos: '1' as unknown as number, secrets: {password: true}, clear: []});
        expect(b.port).toBeUndefined();
        expect(b.qos).toBe(1);
        expect(b.secrets).toBeUndefined();
        expect(b.clear).toBeUndefined();
        expect(headersOf([...headerRows({A: '1'}), {name: ' ', value: 'x'}, {name: ' B ', value: '2'}])).toEqual({A: '1', B: '2'});
    });
});

describe('rules', () => {
    it('starts every source with what the server accepts', () => {
        for (const s of SOURCES) {
            const r = emptyRule(s);
            expect(r.source).toBe(s);
            expect(r.channels).toEqual([]);
        }
        expect(emptyRule('warning').batch_s).toBe(60);
        expect(emptyRule('digest').time).toBe('08:00');
        expect(emptyRule('datapoint').datapoint?.op).toBe('true');
    });
    it('summarises the filter and the channels', () => {
        expect(ruleFilter({...emptyRule('warning'), exclude_ids: ['storage']}, t)).toBe('severity.error, severity.warning · without storage');
        expect(ruleFilter(emptyRule('service'), t)).toBe('kind.unreach, kind.fault, kind.lowbat, kind.other · after 2 min');
        expect(ruleFilter({...emptyRule('datapoint'), datapoint: {interface: 'HmIP-RF', address: 'A:1', key: 'STATE', op: 'gt', value: '21', for_s: 600}}, t)).toBe('A:1 STATE op.gt 21 · for 10 min');
        expect(ruleFilter({...emptyRule('datapoint'), datapoint: {interface: 'HmIP-RF', address: 'A:1', key: 'STATE', op: 'true'}}, t)).toBe('A:1 STATE op.true');
        expect(ruleFilter(emptyRule('inbound'), t)).toBe('every sender');
        expect(ruleFilter({...emptyRule('digest'), only_open: true}, t)).toBe('daily at 08:00 · only when something is open');
        const channels = [{...emptyChannel('ntfy'), id: 'a', name: 'Phone'}];
        expect(ruleChannels(emptyRule('system'), channels, t)).toBe('every channel');
        expect(ruleChannels({...emptyRule('system'), channels: ['a', 'gone']}, channels, t)).toBe('Phone, gone');
        expect([seconds(90), seconds(600), seconds(7200)]).toEqual(['90 s', '10 min', '2 h']);
    });
});
