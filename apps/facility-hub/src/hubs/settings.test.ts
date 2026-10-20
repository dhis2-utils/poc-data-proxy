/**
 * @jest-environment node
 */
import {
    aggregateForm,
    aggregateValue,
    retentionForm,
    retentionValue,
    tokensForm,
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
