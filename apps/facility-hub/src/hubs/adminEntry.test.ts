/**
 * @jest-environment node
 */
import { writeAdminEntry } from './adminEntry'
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
