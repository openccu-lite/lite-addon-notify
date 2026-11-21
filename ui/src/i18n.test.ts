import {readdirSync, readFileSync} from 'node:fs';
import {describe, expect, it} from 'vitest';

import {MESSAGES} from './messages.js';
import {FIELDS, OPS, PRIORITIES, RESULTS, SERVICE_KINDS, SEVERITIES, SOURCES, STEPS, SYSTEM_KINDS} from './model.js';

const has = (key: string) => {
    const e = MESSAGES[key] as {de?: string; en?: string} | undefined;
    return !!e?.de && !!e?.en;
};

// every t('…') of the page has its German and English text
describe('messages', () => {
    it('covers every key of the page', () => {
        const dir = process.cwd() + '/src/';
        const missing: string[] = [];
        for (const f of readdirSync(dir).filter((f) => f.endsWith('.svelte') || f === 'model.ts')) {
            const src = readFileSync(dir + f, 'utf8');
            for (const m of src.matchAll(/\bt\((?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")/g)) {
                const key = (m[1] ?? m[2]!).replace(/\\'/g, "'");
                // a dotted prefix is completed by a value: the lists below cover those
                if (/^[a-z]+\.$/.test(key)) continue;
                if (!has(key)) missing.push(`${f}: ${key}`);
            }
        }
        expect(missing).toEqual([]);
    });
    it('covers the keys built from values', () => {
        const missing: string[] = [];
        const need = (prefix: string, values: readonly string[]) => {
            for (const v of values) if (!has(prefix + v)) missing.push(prefix + v);
        };
        need('step.', STEPS);
        need('result.', RESULTS);
        need('rule.', SOURCES);
        need('severity.', SEVERITIES);
        need('kind.', SERVICE_KINDS);
        need('system.', SYSTEM_KINDS);
        need('op.', OPS);
        need('priority.', PRIORITIES);
        need('source.', ['warnings', 'service', 'system', 'datapoints']);
        for (const fields of Object.values(FIELDS)) {
            for (const f of fields) {
                if (!has(f.label)) missing.push(f.label);
                if (f.help && !has(f.help)) missing.push(f.help);
                for (const o of f.options ?? []) if (!has(o.label)) missing.push(o.label);
            }
        }
        expect(missing).toEqual([]);
    });
    it('never says "box"', () => {
        for (const [k, v] of Object.entries(MESSAGES)) {
            const e = v as {de: string; en: string};
            expect(`${k} ${e.de} ${e.en}`).not.toMatch(/\bbox\b/i);
        }
    });
});
