/**
 * @jest-environment node
 */
import {
    ensureAdminEntries,
    shareAdminEntries,
    writeAdminEntry,
} from './adminEntry'
import { FakeApi } from '@/hubaccount/fakeApi'

// facility-hub-admin must be writable by administrators only (docs/DEPLOYMENT.md): a
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
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
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
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
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
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
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
        expect(api.entry('facility-hub-config', 'retention')).toEqual({
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
        expect(
            (
                api.sharing['meta-facility-hub-admin-accounts'] as {
                    object: { userGroupAccesses: unknown[] }
                }
            ).object.userGroupAccesses
        ).toEqual([])
    })
})
