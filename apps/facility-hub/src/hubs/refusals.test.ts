/**
 * @jest-environment node
 */
import {
    discardChange,
    discardGroup,
    discardOf,
    groupRefusals,
    issueCodes,
    notAppliedOn,
    partlyReported,
    refusedGroupsOf,
    resend,
    Retry,
    scopeOf,
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
    const who = {
        by: 'manager',
        at: '2026-10-20T12:00:00Z',
        id: 'abc',
        orgUnits: ['DistOU00001'],
    }

    it('creates facility-hub-admin/retries with admin-entry sharing', async () => {
        const api = new FakeApi()
        await resend(api, group, { ...who, managers: 'HubGrpMgrs' })
        expect(api.entry('facility-hub-admin', 'retries')).toMatchObject({
            retries: [
                {
                    id: 'abc',
                    ...group,
                    at: who.at,
                    by: 'manager',
                    orgUnits: who.orgUnits,
                },
            ],
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
            {
                id: 'abc',
                ...group,
                at: who.at,
                by: 'manager',
                orgUnits: who.orgUnits,
            },
        ])
    })

    it('names the user to send as, when one is given', async () => {
        const api = new FakeApi()
        await resend(
            api,
            { ...group, code: 'NO_LOGIN' },
            { ...who, user: 'nurse_b' }
        )
        expect(api.entry('facility-hub-admin', 'retries')).toMatchObject({
            retries: [
                {
                    id: 'abc',
                    ...group,
                    code: 'NO_LOGIN',
                    at: who.at,
                    by: 'manager',
                    user: 'nurse_b',
                    orgUnits: who.orgUnits,
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
            orgUnits: who.orgUnits,
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

describe('discardGroup', () => {
    const group = { kind: 'event', program: 'P1', code: 'E1300' }
    const who = {
        by: 'manager',
        at: '2026-11-04T12:00:00Z',
        id: 'd1',
        orgUnits: ['DistOU00001'],
    }

    it('appends a discard of the group to the retries, with admin-entry sharing', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = { retries: { retries: [retry(1)] } }
        await discardGroup(api, group, { ...who, managers: 'HubGrpMgrs' })
        expect(api.entry('facility-hub-admin', 'retries')).toMatchObject({
            retries: [
                retry(1),
                {
                    id: 'd1',
                    action: 'discard',
                    ...group,
                    at: who.at,
                    by: 'manager',
                    orgUnits: who.orgUnits,
                },
            ],
        })
        expect(api.sharing['meta-facility-hub-admin-retries']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
    })
})

describe('discardChange', () => {
    const who = { by: 'manager', at: '2026-11-04T12:00:00Z', id: 'd2' }
    const issue = {
        key: 'hub-a-7',
        hub: 'hub-a',
        kind: 'trackedEntity',
        objectUid: 'DupTe000085',
        codes: ['E1064'],
    }

    it('names the one refused change by its issue key, for one hub', async () => {
        const api = new FakeApi()
        await discardChange(api, issue, who)
        expect(api.entry('facility-hub-admin', 'retries')).toMatchObject({
            retries: [
                {
                    id: 'd2',
                    action: 'discard',
                    issue: 'hub-a-7',
                    hub: 'hub-a',
                    object: 'DupTe000085',
                    kind: 'trackedEntity',
                    program: '',
                    code: 'E1064',
                    at: who.at,
                    by: 'manager',
                },
            ],
        })
    })

    it('is found again by its issue key, and resends are not', async () => {
        const api = new FakeApi()
        await resend(
            api,
            { kind: 'event', program: 'P1', code: 'E1' },
            { ...who, orgUnits: ['DistOU00001'] }
        )
        const list = await discardChange(api, issue, { ...who, id: 'd3' })
        expect(discardOf(list, 'hub-a-7')?.id).toBe('d3')
        expect(discardOf(list, 'hub-a-8')).toBeUndefined()
        expect(
            discardOf([{ ...retry(1), issue: 'hub-a-7' }], 'hub-a-7')
        ).toBeUndefined()
    })
})

describe('issueCodes', () => {
    it('lists each code once, leaving out reports without one', () => {
        expect(
            issueCodes({
                key: 'k',
                error: {
                    errorReports: [
                        { errorCode: 'E1064' },
                        { errorCode: 'E1064' },
                        { message: 'blocked' },
                        { errorCode: 'E5000' },
                    ],
                },
            })
        ).toEqual(['E1064', 'E5000'])
    })
})

// A group instruction is for the hubs the Refusals page counts (review R-30a).
describe('scopeOf', () => {
    const hubs = [
        { orgUnit: { path: '/Root0000001/DistA000001/FacA0000001' } },
        { orgUnit: { path: '/Root0000001/DistB000001/FacB0000001' } },
        {},
    ]
    it("is a manager's own org units", () => {
        expect(
            scopeOf(
                { superuser: false, paths: ['/Root0000001/DistA000001'] },
                hubs
            )
        ).toEqual(['DistA000001'])
    })
    it("is the top of every hub's path for a superuser", () => {
        expect(scopeOf({ superuser: true, paths: [] }, hubs)).toEqual([
            'Root0000001',
        ])
    })
})
