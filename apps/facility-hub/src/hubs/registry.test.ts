/**
 * @jest-environment node
 */
import {
    revokeTokens,
    replacedBy,
    retiredEntry,
    revokedEntry,
    undoReplacement,
    undoneEntries,
} from './registry'
import { FakeApi } from '@/hubaccount/fakeApi'

const at = '2026-10-20T12:00:00.000Z'

describe('registry transitions', () => {
    it('revokes, keeping every other field', () => {
        expect(
            revokedEntry(
                { hubId: 'hub-a', facility: 'F1', tokens: ['t1'], x: 1 },
                'manager',
                at
            )
        ).toEqual({
            hubId: 'hub-a',
            facility: 'F1',
            tokens: ['t1'],
            x: 1,
            status: 'revoked',
            revokedBy: 'manager',
            revokedAt: at,
        })
    })

    it('names the replacing hub only for a replacement, not a reset or an undo', () => {
        const r = { status: 'retired', retiredBy: 'hub-new' }
        expect(replacedBy(r)).toBe('hub-new')
        expect(replacedBy({ ...r, retiredBy: 'reset' })).toBeUndefined()
        expect(replacedBy({ ...r, retiredBy: 'undo' })).toBeUndefined()
        expect(replacedBy({ status: 'active', retiredBy: 'hub-new' })).toBe(
            undefined
        )
    })

    it('undoes a replacement: the old hub active with its history, the new one retired', () => {
        const old = {
            hubId: 'hub-old',
            facility: 'F1',
            status: 'retired',
            retiredBy: 'hub-new',
            retiredByUser: 'setter',
            retiredAt: '2026-10-19T00:00:00Z',
        }
        const replacing = {
            hubId: 'hub-new',
            facility: 'F1',
            status: 'active',
            replaces: ['hub-old'],
        }
        expect(undoneEntries(old, replacing, { by: 'manager', at })).toEqual({
            old: {
                hubId: 'hub-old',
                facility: 'F1',
                status: 'active',
                undone: { by: 'manager', at, retiredBy: 'hub-new' },
            },
            replacing: {
                hubId: 'hub-new',
                facility: 'F1',
                status: 'retired',
                replaces: ['hub-old'],
                retiredBy: 'undo',
                retiredByUser: 'manager',
                retiredAt: at,
            },
        })
    })

    it('a hub retired or revoked counts as retired', () => {
        expect(retiredEntry({ status: 'retired' })).toBe(true)
        expect(retiredEntry({ status: 'revoked' })).toBe(true)
        expect(retiredEntry({ status: 'active' })).toBe(false)
        expect(retiredEntry({})).toBe(false)
    })
})

describe('revokeTokens', () => {
    it('deletes each listed token, counts the ones it could not, and marks the hub revoked', async () => {
        const api = new FakeApi()
        api.apiTokens.add('tok1').add('tok3')
        api.store['facility-hub-registry'] = {
            'hub-a': {
                hubId: 'hub-a',
                facility: 'F1',
                status: 'active',
                tokens: ['tok1', 'tok2', 'tok3'],
            },
        }
        const r = await revokeTokens(api, 'hub-a', { by: 'manager', at })
        expect(r).toEqual({ listed: 3, deleted: 2, failed: ['tok2'] })
        expect([...api.apiTokens]).toEqual([])
        expect(api.entry('facility-hub-registry', 'hub-a')).toEqual({
            hubId: 'hub-a',
            facility: 'F1',
            status: 'revoked',
            tokens: ['tok1', 'tok2', 'tok3'],
            revokedBy: 'manager',
            revokedAt: at,
        })
    })

    it('marks a hub without listed tokens revoked too', async () => {
        const api = new FakeApi()
        api.store['facility-hub-registry'] = {
            'hub-a': { hubId: 'hub-a', status: 'active' },
        }
        const r = await revokeTokens(api, 'hub-a', { by: 'manager', at })
        expect(r).toEqual({ listed: 0, deleted: 0, failed: [] })
        expect(api.entry('facility-hub-registry', 'hub-a')?.status).toBe(
            'revoked'
        )
    })

    it('keeps a change written to the entry while tokens were deleted', async () => {
        const api = new FakeApi()
        api.apiTokens.add('tok1')
        api.store['facility-hub-registry'] = {
            'hub-a': { hubId: 'hub-a', status: 'active', tokens: ['tok1'] },
        }
        api.beforeReread = () => {
            ;(
                api.store['facility-hub-registry']['hub-a'] as {
                    hubVersion?: string
                }
            ).hubVersion = '0.4.0'
        }
        await revokeTokens(api, 'hub-a', { by: 'manager', at })
        expect(api.entry('facility-hub-registry', 'hub-a')).toMatchObject({
            hubVersion: '0.4.0',
            status: 'revoked',
        })
    })
})

describe('undoReplacement', () => {
    const setup = (newStatus = 'active') => {
        const api = new FakeApi()
        api.store['facility-hub-registry'] = {
            'hub-old': {
                hubId: 'hub-old',
                status: 'retired',
                retiredBy: 'hub-new',
                retiredByUser: 'setter',
                retiredAt: '2026-10-19T00:00:00Z',
            },
            'hub-new': {
                hubId: 'hub-new',
                status: newStatus,
                replaces: ['hub-old'],
            },
        }
        return api
    }

    it('writes the replacing hub first, then the old one', async () => {
        const api = setup()
        await undoReplacement(api, 'hub-old', { by: 'manager', at })
        expect(api.log.filter((l) => l.startsWith('PUT'))).toEqual([
            'PUT /dataStore/facility-hub-registry/hub-new',
            'PUT /dataStore/facility-hub-registry/hub-old',
        ])
        expect(api.entry('facility-hub-registry', 'hub-new')).toMatchObject({
            status: 'retired',
            retiredBy: 'undo',
            retiredByUser: 'manager',
        })
        expect(api.entry('facility-hub-registry', 'hub-old')).toEqual({
            hubId: 'hub-old',
            status: 'active',
            undone: { by: 'manager', at, retiredBy: 'hub-new' },
        })
    })

    it('leaves the old hub retired when the replacing hub could not be retired', async () => {
        const api = setup()
        api.failNext['PUT /dataStore/facility-hub-registry/hub-new'] = 403
        await expect(
            undoReplacement(api, 'hub-old', { by: 'manager', at })
        ).rejects.toThrow()
        expect(api.entry('facility-hub-registry', 'hub-old')?.status).toBe(
            'retired'
        )
    })

    it('refuses when the replacing hub is no longer active', async () => {
        const api = setup('revoked')
        await expect(
            undoReplacement(api, 'hub-old', { by: 'manager', at })
        ).rejects.toThrow(/no longer active/)
        expect(api.log.filter((l) => l.startsWith('PUT'))).toEqual([])
    })

    it('refuses a hub not retired by a replacement', async () => {
        const api = setup()
        ;(
            api.store['facility-hub-registry']['hub-old'] as {
                retiredBy: string
            }
        ).retiredBy = 'reset'
        await expect(
            undoReplacement(api, 'hub-old', { by: 'manager', at })
        ).rejects.toThrow(/not retired by a replacement/)
    })
})
