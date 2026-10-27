/**
 * @jest-environment node
 */
import {
    groupRefusals,
    notAppliedOn,
    partlyReported,
    refusedGroupsOf,
    resend,
    Retry,
    withRetry,
} from './refusals'
import { HubRow } from './useHubs'
import { FakeApi } from '@/hubaccount/fakeApi'

const hub = (id: string, extra: Partial<HubRow> = {}): HubRow => ({
    id,
    registry: { hubId: id },
    flags: [],
    retired: false,
    waiting: false,
    ...extra,
})

// One group: [kind, programme, error code, count].
const g = (
    ...[kind, program, code, count]: [string, string, string, number]
) => ({
    kind,
    program,
    code,
    count,
})

describe('refusedGroupsOf', () => {
    it('reads the list from refused (refusedGroups is how many there are)', () => {
        expect(
            refusedGroupsOf({
                refused: [g('event', 'P1', 'E1', 2)],
                refusedGroups: 1,
            })
        ).toEqual([g('event', 'P1', 'E1', 2)])
    })
    it('is empty without a heartbeat or a list', () => {
        expect(refusedGroupsOf(undefined)).toEqual([])
        expect(refusedGroupsOf({ refusedGroups: 3 })).toEqual([])
    })
})

describe('partlyReported', () => {
    it('names the hubs with more groups than their heartbeat lists', () => {
        expect(
            partlyReported([
                hub('hub-a', {
                    heartbeat: {
                        refused: [g('event', 'P1', 'E1', 2)],
                        refusedGroups: 140,
                    },
                }),
                hub('hub-b', {
                    heartbeat: {
                        refused: [g('event', 'P1', 'E1', 2)],
                        refusedGroups: 1,
                    },
                }),
                hub('hub-c', {
                    retired: true,
                    heartbeat: { refused: [], refusedGroups: 140 },
                }),
            ])
        ).toEqual(['hub-a'])
    })
})

describe('groupRefusals', () => {
    it('adds up by kind, programme and code across hubs, leaving retired and revoked hubs out', () => {
        const rows = groupRefusals([
            hub('hub-a', {
                heartbeat: {
                    refused: [
                        g('event', 'P1', 'E1300', 2),
                        g('enrollment', 'P1', 'E1300', 1),
                    ],
                },
            }),
            hub('hub-b', {
                heartbeat: {
                    refused: [
                        g('event', 'P1', 'E1300', 5),
                        g('event', '', 'none', 1),
                    ],
                },
            }),
            hub('hub-old', {
                retired: true,
                heartbeat: { refused: [g('event', 'P1', 'E1300', 9)] },
            }),
            hub('hub-quiet'),
        ])
        expect(rows).toEqual([
            {
                kind: 'event',
                program: 'P1',
                code: 'E1300',
                count: 7,
                hubs: ['hub-a', 'hub-b'],
            },
            {
                kind: 'enrollment',
                program: 'P1',
                code: 'E1300',
                count: 1,
                hubs: ['hub-a'],
            },
            {
                kind: 'event',
                program: '',
                code: 'none',
                count: 1,
                hubs: ['hub-b'],
            },
        ])
    })
})

const retry = (n: number): Retry => ({
    id: `r${n}`,
    kind: 'event',
    program: 'P1',
    code: 'E1300',
    at: `2026-10-20T00:00:${String(n % 60).padStart(2, '0')}Z`,
    by: 'manager',
})

describe('withRetry', () => {
    it('appends and keeps the last 50', () => {
        const list = Array.from({ length: 50 }, (_, i) => retry(i))
        const next = withRetry(list, retry(50))
        expect(next).toHaveLength(50)
        expect(next[0].id).toBe('r1')
        expect(next[49].id).toBe('r50')
        expect(withRetry([], retry(1))).toEqual([retry(1)])
    })
})

describe('resend', () => {
    const group = { kind: 'event', program: 'P1', code: 'E1300' }
    const who = { by: 'manager', at: '2026-10-20T12:00:00Z', id: 'abc' }

    it('creates facility-hub-admin/retries with admin-entry sharing', async () => {
        const api = new FakeApi()
        await resend(api, group, { ...who, managers: 'HubGrpMgrs' })
        expect(api.entry('facility-hub-admin', 'retries')).toEqual({
            retries: [{ id: 'abc', ...group, at: who.at, by: 'manager' }],
        })
        expect(api.sharing['meta-facility-hub-admin-retries']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
    })

    it('appends to the retries already there', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = { retries: { retries: [retry(1)] } }
        await resend(api, group, who)
        expect(
            (api.entry('facility-hub-admin', 'retries') as { retries: Retry[] })
                .retries
        ).toEqual([
            retry(1),
            { id: 'abc', ...group, at: who.at, by: 'manager' },
        ])
    })

    it('names the user to send as, when one is given', async () => {
        const api = new FakeApi()
        await resend(
            api,
            { ...group, code: 'NO_LOGIN' },
            { ...who, user: 'nurse_b' }
        )
        expect(api.entry('facility-hub-admin', 'retries')).toEqual({
            retries: [
                {
                    id: 'abc',
                    ...group,
                    code: 'NO_LOGIN',
                    at: who.at,
                    by: 'manager',
                    user: 'nurse_b',
                },
            ],
        })
    })

    it('leaves the user out when none is given', async () => {
        const api = new FakeApi()
        await resend(api, group, { ...who, user: '  ' })
        expect(
            (api.entry('facility-hub-admin', 'retries') as { retries: Retry[] })
                .retries[0]
        ).not.toHaveProperty('user')
    })

    it('copies a group without a programme exactly', async () => {
        const api = new FakeApi()
        await resend(api, { kind: 'aggregate', program: '', code: 'none' }, who)
        expect(
            (api.entry('facility-hub-admin', 'retries') as { retries: Retry[] })
                .retries[0]
        ).toEqual({
            id: 'abc',
            kind: 'aggregate',
            program: '',
            code: 'none',
            at: who.at,
            by: 'manager',
        })
    })
})

describe('notAppliedOn', () => {
    it('lists the hubs that did not apply a retry, with their reason', () => {
        expect(
            notAppliedOn(
                [
                    hub('hub-a', {
                        heartbeat: {
                            retriesNotApplied: [
                                {
                                    id: 'r1',
                                    reason: 'user x is not known on this hub',
                                },
                            ],
                        },
                    }),
                    hub('hub-b', { heartbeat: { retriesNotApplied: [] } }),
                    hub('hub-c'),
                ],
                'r1'
            )
        ).toEqual([{ hub: 'hub-a', reason: 'user x is not known on this hub' }])
    })
})
