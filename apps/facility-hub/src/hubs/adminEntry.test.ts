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
        await writeAdminEntry(api, 'central-move', {
            url: 'https://new.example.org',
        })
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
            url: 'https://new.example.org',
        })
        expect(api.sharing['meta-facility-hub-admin-central-move']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [],
            },
        })
    })

    it('restricts an existing entry too', async () => {
        const api = new FakeApi()
        api.store['facility-hub-admin'] = { 'central-move': { url: 'x' } }
        await writeAdminEntry(api, 'central-move', {
            url: 'https://new.example.org',
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
        await writeAdminEntry(api, 'central-move', {
            url: 'https://new.example.org',
        })
        expect(api.creators['facility-hub-admin/central-move']).toBe(api.me)
        expect(api.entry('facility-hub-admin', 'central-move')).toEqual({
            url: 'https://new.example.org',
        })
    })
})
