/**
 * @jest-environment node
 */
import { gcm } from '@noble/ciphers/aes.js'
import { x25519 } from '@noble/curves/ed25519.js'
import { hkdf } from '@noble/hashes/hkdf.js'
import { sha256 } from '@noble/hashes/sha2.js'
import { utf8ToBytes } from '@noble/hashes/utils.js'
import { approve, fromBase64, toBase64 } from './approve'
import { accountCode } from './codes'
import { FakeApi } from './fakeApi'

const hubID = 'hub-Abc123XyZ90'
const facility = 'DiszpKrYNg8'
const hubPriv = new Uint8Array(32).fill(7)
const hubPub = x25519.getPublicKey(hubPriv)
const opts = { group: 'HubGrpAccts', role: 'HubRoleAcct' }

// open is the hub's side (internal/hubaccount.Open), for checking what was sealed.
const open = (box: Uint8Array) => {
    const key = hkdf(
        sha256,
        x25519.getSharedSecret(hubPriv, box.slice(0, 32)),
        undefined,
        utf8ToBytes(`facility-hub account box ${hubID}`),
        32
    )
    const plain = gcm(
        key,
        box.slice(32, 44),
        utf8ToBytes(`hub-account:${hubID}`)
    ).decrypt(box.slice(44))
    return JSON.parse(new TextDecoder().decode(plain))
}

const waiting = () => {
    const api = new FakeApi()
    api.store['facility-hub-registry'] = {
        [hubID]: {
            hubId: hubID,
            facility,
            status: 'active',
            accountRequested: true,
            boxKey: toBase64(hubPub),
            setupBy: 'nurse',
        },
    }
    return api
}

const code = accountCode(hubPub, hubID, facility)

describe('approve', () => {
    it('gives a waiting hub its account, as FACILITY-HUB-APP.md describes', async () => {
        const api = waiting()
        const steps: string[] = []
        await approve(
            api,
            { hubID, code: code.toLowerCase() },
            { ...opts, onStep: (s) => steps.push(s) }
        )

        const user = Object.values(api.users)[0]
        expect(user.username).toBe(hubID)
        expect(user.organisationUnits).toEqual([facility])
        expect(user.dataViewOrganisationUnits).toEqual([facility])
        expect(user.teiSearchOrganisationUnits).toEqual([facility])
        expect(user.roles).toEqual([opts.role])
        expect(user.groups).toEqual([opts.group])

        const reg = api.entry('facility-hub-registry', hubID)!
        expect(reg.setupBy).toBe('nurse') // the rest of the entry is kept
        const account = reg.account as { username: string; box: string }
        expect(account.username).toBe(hubID)
        const creds = open(fromBase64(account.box))
        expect(creds.username).toBe(hubID)
        expect(creds.password).toBe(user.password)
        expect(creds.password).toMatch(
            /^(?=.*[a-z])(?=.*[A-Z])(?=.*\d)(?=.*[^A-Za-z\d]).{8,}$/
        )

        const key = api.entry('facility-hub-keys', hubID)!
        expect(key.moveKey).toBe(creds.moveKey)
        expect(fromBase64(creds.moveKey as string)).toHaveLength(32)
        expect(api.sharing[`meta-facility-hub-keys-${hubID}`]).toEqual({
            object: {
                publicAccess: '--------',
                userAccesses: [],
                userGroupAccesses: [],
            },
        })
        expect(steps.length).toBeGreaterThan(3)
    })

    it('writes the move key only once its entry is restricted', async () => {
        const api = waiting()
        await approve(api, { hubID, code }, opts)
        const keyWrites = api.log.filter(
            (l) => l.includes('facility-hub-keys') || l === 'POST /sharing'
        )
        expect(keyWrites).toEqual([
            `DELETE /dataStore/facility-hub-keys/${hubID}`,
            `POST /dataStore/facility-hub-keys/${hubID}`,
            `GET /dataStore/facility-hub-keys/${hubID}/metaData`,
            'POST /sharing',
            `PUT /dataStore/facility-hub-keys/${hubID}`,
        ])
    })

    it("refuses a code that is not the hub's", async () => {
        const api = waiting()
        await expect(
            approve(api, { hubID, code: 'AAAA-AAAA-AAAA-AAAA' }, opts)
        ).rejects.toThrow(/code/)
        expect(api.users).toEqual({})
    })

    it('refuses an entry whose facility was changed', async () => {
        const api = waiting()
        ;(
            api.entry('facility-hub-registry', hubID) as Record<string, unknown>
        ).facility = 'g8upMTyEZGZ'
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(
            /code/
        )
    })

    it('refuses a hub that is not waiting', async () => {
        const api = waiting()
        delete (
            api.entry('facility-hub-registry', hubID) as Record<string, unknown>
        ).accountRequested
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(
            /not waiting/i
        )
    })

    it('refuses a hub account role with authorities', async () => {
        const api = waiting()
        api.roleAuthorities[opts.role] = ['F_TRACKED_ENTITY_ADD']
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(
            /authorities/
        )
        expect(api.users).toEqual({})
    })

    const existing = (
        api: FakeApi,
        change: Partial<Record<string, unknown>> = {}
    ) => {
        api.users.HubUser0001 = {
            id: 'HubUser0001',
            username: hubID,
            password: 'old',
            disabled: false,
            organisationUnits: [facility],
            dataViewOrganisationUnits: [],
            teiSearchOrganisationUnits: [facility],
            roles: [opts.role],
            groups: [opts.group],
            ...change,
        }
    }

    it('gives a fit existing account a new password', async () => {
        const api = waiting()
        existing(api)
        await approve(api, { hubID, code }, opts)
        expect(Object.keys(api.users)).toEqual(['HubUser0001'])
        expect(api.users.HubUser0001.password).not.toBe('old')
        const reg = api.entry('facility-hub-registry', hubID)!
        expect(
            open(fromBase64((reg.account as { box: string }).box)).password
        ).toBe(api.users.HubUser0001.password)
    })

    it.each([
        ['disabled', { disabled: true }, /disabled/],
        [
            'wider',
            { dataViewOrganisationUnits: ['FakeDistr01'] },
            /facility only/,
        ],
        ['with authorities', { roles: ['Superuser'] }, /authorities/],
    ])('refuses an existing account that is %s', async (_name, change, why) => {
        const api = waiting()
        api.roleAuthorities.Superuser = ['ALL']
        existing(api, change)
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(why)
        expect(api.users.HubUser0001.password).toBe('old')
    })

    it('refuses when the entry changed meanwhile', async () => {
        const api = waiting()
        api.beforeReread = () => {
            ;(
                api.entry('facility-hub-registry', hubID) as Record<
                    string,
                    unknown
                >
            ).account = { box: 'x' }
        }
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(
            /changed meanwhile/
        )
    })

    it('names the step that failed', async () => {
        const api = waiting()
        api.failNext['POST /sharing'] = 500
        await expect(approve(api, { hubID, code }, opts)).rejects.toThrow(
            /limiting its move key/i
        )
        expect(api.entry('facility-hub-keys', hubID)!.moveKey).toBeUndefined()
    })
})
