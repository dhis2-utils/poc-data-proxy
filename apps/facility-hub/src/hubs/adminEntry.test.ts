/**
 * @jest-environment node
 */
import {
    addHubCodes,
    configKeys,
    ensureAdminEntries,
    shareAccountsGroup,
    shareAdminEntries,
    writeAdminEntry,
} from './adminEntry'
import { toBase64 } from '@/hubaccount/approve'
import { instructionCode } from '@/hubaccount/codes'
import { FakeApi } from '@/hubaccount/fakeApi'

// facility-hub-admin must be writable by administrators only (docs/OPERATIONS.md): a
// planned move there moves every hub. A new dataStore entry is public read and write
// until its sharing is set.
describe('writeAdminEntry', () => {
    it('creates the entry readable by all, writable by no one but administrators', async () => {
        const api = new FakeApi()
        await writeAdminEntry(api, {
            key: 'central-move',
            value: { url: 'https://new.example.org' },
            managers: 'HubGrpMgrs',
        })
        expect(api.entry('facility-hub-admin', 'central-move')).toMatchObject({
            url: 'https://new.example.org',
        })
        expect(api.sharing['meta-facility-hub-admin-central-move']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
    })

    it('restricts an existing entry too', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = { 'central-move': { url: 'x' } }
        await writeAdminEntry(api, {
            key: 'central-move',
            value: { url: 'https://new.example.org' },
            managers: 'HubGrpMgrs',
        })
        expect(api.entry('facility-hub-admin', 'central-move')).toMatchObject({
            url: 'https://new.example.org',
        })
        expect(
            api.sharing['meta-facility-hub-admin-central-move']
        ).toBeDefined()
    })

    it('takes over an entry someone else created first, who would keep writing it as its owner', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = {
            'central-move': { url: 'https://evil.example' },
        }
        api.creators['facility-hub-admin/central-move'] = 'SomeoneElse'
        await writeAdminEntry(api, {
            key: 'central-move',
            value: { url: 'https://new.example.org' },
            managers: 'HubGrpMgrs',
        })
        expect(api.creators['facility-hub-admin/central-move']).toBe(api.me)
        expect(api.entry('facility-hub-admin', 'central-move')).toMatchObject({
            url: 'https://new.example.org',
        })
    })
})

// The country's settings (facility-hub-config) are written by managers with the same
// sharing (spec 2026-10-20, section 4).
describe('writeAdminEntry in facility-hub-config', () => {
    it('writes the entry with the same sharing as facility-hub-admin', async () => {
        const api = new FakeApi()
        api.store['facility-hub-config'] = {
            retention: { defaultMonths: 99 },
        }
        api.creators['facility-hub-config/retention'] = 'AHubOwner01'
        await writeAdminEntry(api, {
            namespace: 'facility-hub-config',
            key: 'retention',
            value: { defaultMonths: 12 },
            managers: 'HubGrpMgrs',
        })
        expect(api.entry('facility-hub-config', 'retention')).toMatchObject({
            defaultMonths: 12,
        })
        expect(api.creators['facility-hub-config/retention']).toBe(api.me)
        expect(api.sharing['meta-facility-hub-config-retention']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
        expect(api.store['facility-hub-admin']).toBeUndefined()
    })
})

// On 2.42 any user may create a missing dataStore key, own it, and keep writing it
// while public access is read only: the hub would trust it. A manager opening the app
// creates the missing facility-hub-admin entries first, protected.
describe('ensureAdminEntries', () => {
    const keys = ['accounts', 'central-move', 'release', 'retries']

    it('creates each missing entry, readable by all, writable by the managers', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R', managers: 'HubGrpMgrs' },
        }
        api.sharing['meta-facility-hub-admin-accounts'] = {
            object: { publicAccess: 'r-------' },
        }
        expect(await ensureAdminEntries(api, 'HubGrpMgrs')).toEqual([
            'central-move',
            'release',
            'retries',
        ])
        // Not the settings: a hub not yet approved uses the defaults.
        for (const key of configKeys) {
            expect(api.store['facility-hub-config']?.[key]).toBeUndefined()
        }
        for (const key of keys.slice(1)) {
            expect(api.creators[`facility-hub-admin/${key}`]).toBe(api.me)
            expect(api.sharing[`meta-facility-hub-admin-${key}`]).toEqual({
                object: {
                    publicAccess: 'r-------',
                    userAccesses: [],
                    userGroupAccesses: [
                        { id: 'HubGrpMgrs', access: 'rw------' },
                    ],
                },
            })
        }
        // Empty values the hubs and the app read as "not set".
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({})
        expect(api.entry('facility-hub-admin', 'release')).toEqual({
            version: '',
            pilotHubs: [],
            percent: 0,
        })
        expect(api.entry('facility-hub-admin', 'retries')).toEqual({
            retries: [],
        })
        expect(api.entry('facility-hub-admin', 'accounts')).toEqual({
            group: 'G',
            role: 'R',
            managers: 'HubGrpMgrs',
        })
    })

    it('creates accounts writable by administrators only', async () => {
        const api = new FakeApi()
        await ensureAdminEntries(api, undefined)
        expect(api.entry('facility-hub-admin', 'accounts')).toEqual({})
        expect(api.sharing['meta-facility-hub-admin-accounts']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [],
            },
        })
    })

    it('does nothing when all exist', async () => {
        const api = new FakeApi()
        await ensureAdminEntries(api, 'HubGrpMgrs')
        api.log = []
        expect(await ensureAdminEntries(api, 'HubGrpMgrs')).toEqual([])
        expect(api.log.filter((l) => !l.startsWith('GET '))).toEqual([])
    })
})

// Entries made before the hub managers group was named are writable by
// administrators only: naming the group shares them with it.
describe('shareAdminEntries', () => {
    it('shares the protected entries with the managers, and leaves open ones as they are', async () => {
        const api = new FakeApi()
        await ensureAdminEntries(api, undefined)
        await writeAdminEntry(api, {
            namespace: 'facility-hub-config',
            key: 'retention',
            value: {},
            sign: false,
        }) // a setting saved before the group was named
        api.store['facility-hub-admin'].retries = { retries: [] }
        delete api.sharing['meta-facility-hub-admin-retries'] // open to all
        await shareAdminEntries(api, 'HubGrpMgrs')
        expect(api.sharing['meta-facility-hub-admin-release']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
        expect(api.sharing['meta-facility-hub-admin-retries']).toBeUndefined()
        expect(api.sharing['meta-facility-hub-config-retention']).toEqual(
            api.sharing['meta-facility-hub-admin-release']
        )
        expect(
            (
                api.sharing['meta-facility-hub-admin-accounts'] as {
                    object: { userGroupAccesses: unknown[] }
                }
            ).object.userGroupAccesses
        ).toEqual([])
    })
})

// Every instruction the app writes carries a code for each approved hub, made with its
// move key, which the hub checks (internal/core/instructions.go; the shared vector is
// in codes.test.ts).
describe('signed instructions', () => {
    const keyA = new Uint8Array(32).fill(1)
    const keyB = new Uint8Array(32).fill(2)
    const withKeys = (api: FakeApi, keys: Record<string, Uint8Array>) => {
        api.store['facility-hub-keys'] = Object.fromEntries(
            Object.entries(keys).map(([h, k]) => [h, { moveKey: toBase64(k) }])
        )
    }

    it('signs what it writes for every hub whose move key it reads', async () => {
        const api = new FakeApi()
        withKeys(api, { 'hub-a': keyA, 'hub-b': keyB })
        await writeAdminEntry(api, {
            key: 'release',
            value: { version: '0.3.0', codes: { 'hub-x': 'old' } },
        })
        const v = api.entry('facility-hub-admin', 'release') as {
            issued: string
            codes: Record<string, string>
        }
        expect(Date.parse(v.issued)).not.toBeNaN()
        expect(Object.keys(v.codes).sort()).toEqual(['hub-a', 'hub-b'])
        expect(v.codes['hub-a']).toBe(
            instructionCode(keyA, 'hub-a', {
                kind: 'facility-hub-admin/release',
                obj: v,
            })
        )
        expect(v.codes['hub-b']).toBe(
            instructionCode(keyB, 'hub-b', {
                kind: 'facility-hub-admin/release',
                obj: v,
            })
        )
    })

    it("adds a newly approved hub's code to what it signed before, and to nothing else", async () => {
        const api = new FakeApi()
        withKeys(api, { 'hub-a': keyA })
        await writeAdminEntry(api, { key: 'release', value: { version: '1' } })
        const before = api.entry('facility-hub-admin', 'release') as {
            issued: string
            codes: Record<string, string>
        }
        api.store['facility-hub-admin']['central-move'] = {
            url: 'https://unsigned',
        }
        api.store['facility-hub-config'] = {
            retention: { defaultMonths: 1, codes: { 'hub-a': 'forged' } },
        }
        withKeys(api, { 'hub-a': keyA, 'hub-b': keyB })
        await addHubCodes(api, 'hub-b')
        const after = api.entry('facility-hub-admin', 'release') as {
            issued: string
            codes: Record<string, string>
        }
        expect(after.issued).toBe(before.issued)
        expect(after.codes['hub-a']).toBe(before.codes['hub-a'])
        expect(after.codes['hub-b']).toBe(
            instructionCode(keyB, 'hub-b', {
                kind: 'facility-hub-admin/release',
                obj: after,
            })
        )
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
            url: 'https://unsigned',
        })
        expect(api.entry('facility-hub-config', 'retention')).toEqual({
            defaultMonths: 1,
            codes: { 'hub-a': 'forged' },
        })
    })
})

// A hub shares its registry entry with the hub accounts group as whoever set it up, and
// DHIS2 2.42 drops from a sharing a group the sender cannot see.
describe('shareAccountsGroup', () => {
    it('makes the hub accounts group readable by the owners and managers, keeping its sharing', async () => {
        const api = new FakeApi()
        const id = 'HubGrpAccts'
        api.sharing[id] = {
            object: {
                publicAccess: '--------',
                userAccesses: [{ id: 'SomeUser001', access: 'rw------' }],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'r-------' }],
            },
        }
        await shareAccountsGroup(api, {
            group: id,
            managers: 'HubGrpMgrs',
            owners: 'HubGrpOwnr',
        })
        expect(api.sharing[id]).toEqual({
            object: {
                publicAccess: '--------',
                externalAccess: false,
                userAccesses: [{ id: 'SomeUser001', access: 'rw------' }],
                userGroupAccesses: [
                    { id: 'HubGrpMgrs', access: 'r-------' },
                    { id: 'HubGrpOwnr', access: 'r-------' },
                ],
            },
        })
        // Nothing to add: nothing written.
        api.log = []
        await shareAccountsGroup(api, {
            group: id,
            managers: 'HubGrpMgrs',
            owners: 'HubGrpOwnr',
        })
        expect(api.log).toEqual(['GET /sharing'])
    })

    it('shares with neither group while none is named', async () => {
        const api = new FakeApi()
        await shareAccountsGroup(api, { group: 'HubGrpAccts' })
        expect(api.log).toEqual(['GET /sharing'])
    })
})
