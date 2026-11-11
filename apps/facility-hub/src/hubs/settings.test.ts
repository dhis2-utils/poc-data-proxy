/**
 * @jest-environment node
 */
import {
    aggregateForm,
    aggregateValue,
    localReadsValue,
    metadataReloadValue,
    retentionForm,
    retentionValue,
    tokensForm,
    poolForm,
    poolValue,
    tokensValue,
    wholeNumber,
} from './settings'

describe('wholeNumber', () => {
    const l = { min: 7, max: 365 }
    it.each([
        ['7', 7],
        [' 90 ', 90],
        ['365', 365],
        ['6', null],
        ['366', null],
        ['', null],
        ['9.5', null],
        ['1e2', null],
        ['-10', null],
        ['abc', null],
    ])('%j is %j', (s, want) => {
        expect(wholeNumber(s, l)).toBe(want)
    })
})

describe('tracker retention', () => {
    it('shows the defaults for a missing key: keep everything', () => {
        expect(retentionForm(null)).toEqual({
            defaultMonths: '0',
            programs: [],
            trackedEntityTypes: [],
        })
    })
    it('round-trips a stored value, keeping fields it does not edit', () => {
        const stored = {
            defaultMonths: 24,
            programs: { IpHINAT79UW: 6, lxAQ7Zs9VYR: 0 },
            trackedEntityTypes: { nEenWmSyUEp: 36 },
            note: 'kept',
        }
        const form = retentionForm(stored)
        expect(form.programs).toEqual([
            { id: 'IpHINAT79UW', value: '6' },
            { id: 'lxAQ7Zs9VYR', value: '0' },
        ])
        expect(retentionValue(form, stored)).toEqual(stored)
    })
    it('refuses months that are not whole numbers from 0 to 1200', () => {
        const form = retentionForm(null)
        expect(retentionValue({ ...form, defaultMonths: '-1' }, null)).toBe(
            null
        )
        expect(
            retentionValue(
                { ...form, programs: [{ id: 'IpHINAT79UW', value: '2.5' }] },
                null
            )
        ).toBe(null)
        expect(
            retentionValue(
                {
                    ...form,
                    trackedEntityTypes: [{ id: 'nEenWmSyUEp', value: '1201' }],
                },
                null
            )
        ).toBe(null)
    })
})

describe('aggregate retention', () => {
    it('shows 12 periods for a missing key', () => {
        expect(aggregateForm(null)).toEqual({
            retentionPeriods: '12',
            dataSets: [],
        })
    })
    it('shows the default for a stored 0, which the hubs read as the default', () => {
        expect(
            aggregateForm({ retentionPeriods: 0, dataSets: { BfMAe6Itzgt: 0 } })
        ).toEqual({ retentionPeriods: '12', dataSets: [] })
    })
    it('writes periods by data set; at least one period', () => {
        const form = {
            retentionPeriods: '6',
            dataSets: [{ id: 'BfMAe6Itzgt', value: '24' }],
        }
        expect(aggregateValue(form, null)).toEqual({
            retentionPeriods: 6,
            dataSets: { BfMAe6Itzgt: 24 },
        })
        expect(aggregateValue({ ...form, retentionPeriods: '0' }, null)).toBe(
            null
        )
    })
})

describe('token lifetime', () => {
    it('shows 90 days for a missing key', () => {
        expect(tokensForm(null)).toBe('90')
    })
    it('keeps it within 7 to 365 days', () => {
        expect(tokensValue('30', { other: 1 })).toEqual({
            other: 1,
            expiryDays: 30,
        })
        expect(tokensValue('6', null)).toBe(null)
        expect(tokensValue('366', null)).toBe(null)
    })
})

describe('reserved-value pool', () => {
    it('shows 500 for a missing key', () => {
        expect(poolForm(null)).toEqual({ defaultPool: '500', attributes: [] })
    })
    it('round-trips a stored value, keeping fields it does not edit', () => {
        const stored = {
            default: 800,
            attributes: { xs8A6tQJY0s: 3000 },
            other: 1,
        }
        expect(poolForm(stored)).toEqual({
            defaultPool: '800',
            attributes: [{ id: 'xs8A6tQJY0s', value: '3000' }],
        })
        expect(poolValue(poolForm(stored), stored)).toEqual(stored)
    })
    it('writes the default and per-attribute overrides; blanks are left out', () => {
        const form = {
            defaultPool: '500',
            attributes: [
                { id: 'xs8A6tQJY0s', value: '3000' },
                { id: 'AuPLng5hLbE', value: '' },
            ],
        }
        expect(poolValue(form, null)).toEqual({
            default: 500,
            attributes: { xs8A6tQJY0s: 3000 },
        })
        expect(poolValue({ ...form, defaultPool: '0' }, null)).toBe(null)
        expect(poolValue({ ...form, defaultPool: '5000' }, null)).toEqual({
            default: 5000,
            attributes: { xs8A6tQJY0s: 3000 },
        })
        expect(poolValue({ ...form, defaultPool: '5001' }, null)).toBe(null)
        expect(
            poolValue(
                { ...form, attributes: [{ id: 'x', value: '5001' }] },
                null
            )
        ).toBe(null)
        expect(
            poolValue(
                { ...form, attributes: [{ id: 'x', value: 'many' }] },
                null
            )
        ).toBe(null)
    })
    it("round-trips a stored 0 per attribute: that attribute's pool is off", () => {
        const stored = { default: 500, attributes: { xs8A6tQJY0s: 0 } }
        const form = poolForm(stored)
        expect(form.attributes).toEqual([{ id: 'xs8A6tQJY0s', value: '0' }])
        expect(poolValue(form, stored)).toEqual(stored)
    })
    it('keeps -1 invalid per attribute, and the default field rejects 0', () => {
        const form = { defaultPool: '500', attributes: [] }
        expect(
            poolValue({ ...form, attributes: [{ id: 'x', value: '-1' }] }, null)
        ).toBe(null)
        expect(poolValue({ ...form, defaultPool: '0' }, null)).toBe(null)
    })
})

describe('localReadsValue', () => {
    it('writes the chosen programmes, sorted, keeping other fields', () => {
        expect(
            localReadsValue(['ProgB', 'ProgA'], {
                programs: ['Old'],
                note: 'x',
            })
        ).toEqual({ programs: ['ProgA', 'ProgB'], note: 'x' })
    })
    it('writes an empty list for none', () => {
        expect(localReadsValue([], null)).toEqual({ programs: [] })
    })
})

describe('metadataReloadValue', () => {
    it('writes the time, keeping other fields', () => {
        expect(
            metadataReloadValue(new Date('2026-11-06T13:00:00Z'), {
                at: 'old',
                note: 'x',
            })
        ).toEqual({ at: '2026-11-06T13:00:00.000Z', note: 'x' })
    })
})
