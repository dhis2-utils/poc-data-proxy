import { randomBytes, utf8ToBytes } from '@noble/hashes/utils.js'
import { Api, ApiError } from './api'
import { seal } from './box'
import { accountCode, normalizeCode } from './codes'

// Approving a hub's own account (docs/FACILITY-HUB-APP.md), as the reference does
// (internal/hubaccount/approve.go): the steps, their order and their refusals are the
// contract with the hub.

export const toBase64 = (bytes: Uint8Array): string =>
    btoa(Array.from(bytes, (b) => String.fromCharCode(b)).join(''))

/** Standard base64 with padding, strictly (as Go's base64.StdEncoding): anything else is empty. */
export const fromBase64 = (s: string): Uint8Array =>
    // atob would also take it unpadded, with spaces, or with = inside.
    s.length % 4 === 0 && /^[A-Za-z0-9+/]*={0,2}$/.test(s)
        ? Uint8Array.from(atob(s), (c) => c.charCodeAt(0))
        : new Uint8Array(0)

const alnum = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
const token = (n: number) =>
    Array.from(randomBytes(n), (b) => alnum[b % alnum.length]).join('')

/** A password meeting DHIS2's rules: upper and lower case, a digit, a special character. */
export const newPassword = (): string => `Hub-${token(28)}9a!`

const str = (v: unknown) => (typeof v === 'string' ? v : '')

type Ref = { id: string; authorities?: string[] }
type ExistingUser = {
    id: string
    disabled?: boolean
    organisationUnits?: Ref[]
    dataViewOrganisationUnits?: Ref[]
    teiSearchOrganisationUnits?: Ref[]
    userRoles?: Ref[]
}

export type ApproveOptions = {
    /** The user group of hub accounts (write sharing on the facility-hub namespaces). */
    group: string
    /** The user role hub accounts get: it must have no authorities. */
    role: string
    /** The hub managers group, which shares the move keys (missing: no one). */
    managers?: string
    /** Called as each step starts, with what it does. */
    onStep?: (step: string) => void
}

// step runs one step, naming it in its error.
const step = async <T>(
    name: string,
    fn: () => Promise<T>,
    onStep?: (s: string) => void
): Promise<T> => {
    onStep?.(name)
    try {
        return await fn()
    } catch (e) {
        throw new Error(`${name}: ${(e as Error).message}`)
    }
}

/**
 * Gives a waiting hub an account of its own, checked against the code the facility
 * read from the hub's page. Throws with the reason or the step that failed; running
 * it again is safe.
 */
export const approve = async (
    api: Api,
    { hubID, code }: { hubID: string; code: string },
    { group, role, managers, onStep }: ApproveOptions
): Promise<void> => {
    const regPath = `/dataStore/facility-hub-registry/${encodeURIComponent(hubID)}`
    const entry = (await step(
        'Reading its registry entry',
        () => api.request('GET', regPath),
        onStep
    )) as Record<string, unknown>
    const pub = fromBase64(str(entry.boxKey))
    const facility = str(entry.facility)
    if (entry.accountRequested !== true || entry.account != null) {
        throw new Error('Not waiting for an account')
    }
    if (entry.status !== 'active') {
        throw new Error('Not active')
    }
    if (!facility) {
        throw new Error('No facility')
    }
    if (pub.length !== 32) {
        throw new Error('No key to hand the account over with')
    }
    if (
        normalizeCode(accountCode(pub, hubID, facility)) !== normalizeCode(code)
    ) {
        throw new Error(
            "The code is not the one for this hub's key and facility: ask the facility to read it again from the hub's page"
        )
    }

    const moveKey = randomBytes(32)
    const creds = {
        username: hubID,
        password: newPassword(),
        moveKey: toBase64(moveKey),
    }
    await account(api, { creds, facility, group, role, onStep })

    const keyPath = `/dataStore/facility-hub-keys/${encodeURIComponent(hubID)}`
    await step(
        'Removing its old move key',
        async () => {
            try {
                await api.request('DELETE', keyPath)
            } catch (e) {
                if (!(e instanceof ApiError && e.status === 404)) {
                    throw e
                }
            }
        },
        onStep
    )
    // A new entry is public (read and write) until its sharing is set: the key goes in
    // only after.
    await step(
        'Making the entry for its move key',
        () => api.request('POST', keyPath, {}),
        onStep
    )
    const meta = (await step(
        'Reading that entry',
        () => api.request('GET', `${keyPath}/metaData`),
        onStep
    )) as {
        id: string
    }
    await step(
        'Limiting its move key to the hub managers',
        () =>
            api.request(
                'POST',
                `/sharing?type=dataStore&id=${encodeURIComponent(meta.id)}`,
                {
                    object: {
                        publicAccess: '--------',
                        userAccesses: [],
                        // The hub managers make move codes and approve again; no one else.
                        userGroupAccesses: managers
                            ? [{ id: managers, access: 'rw------' }]
                            : [],
                    },
                }
            ),
        onStep
    )
    await step(
        'Keeping its move key',
        () => api.request('PUT', keyPath, { moveKey: toBase64(moveKey) }),
        onStep
    )

    const box = seal({
        pub,
        plaintext: utf8ToBytes(JSON.stringify(creds)),
        hubID,
    })
    // Read again just before writing, and change only the account: the hub writes its
    // entry too.
    const now = (await step(
        'Reading its registry entry again',
        () => api.request('GET', regPath),
        onStep
    )) as Record<string, unknown>
    if (
        str(now.boxKey) !== str(entry.boxKey) ||
        str(now.facility) !== facility ||
        now.account != null
    ) {
        throw new Error(
            'Its entry changed meanwhile (another approval?): ask the facility for the code again'
        )
    }
    await step(
        'Handing the account over',
        () =>
            api.request('PUT', regPath, {
                ...now,
                account: { username: hubID, box: toBase64(box) },
            }),
        onStep
    )
}

// account creates the hub's user, or gives the existing one (approved before) a new
// password. An existing account the hub would refuse is not handed over again: the
// approver is told what to correct.
const account = async (
    api: Api,
    {
        creds,
        facility,
        group,
        role,
        onStep,
    }: {
        creds: { username: string; password: string }
        facility: string
        group: string
        role: string
        onStep?: (s: string) => void
    }
): Promise<string> => {
    const r = (await step(
        'Reading the hub account role',
        () =>
            api.request(
                'GET',
                `/userRoles/${encodeURIComponent(role)}?fields=id,authorities`
            ),
        onStep
    )) as Ref
    if (r.authorities?.length) {
        throw new Error(
            `The hub account role has authorities (${r.authorities.join(', ')}): it must have none`
        )
    }
    const found = (await step(
        'Looking for its account',
        () =>
            api.request(
                'GET',
                `/users?filter=username:eq:${encodeURIComponent(creds.username)}` +
                    '&fields=id,disabled,organisationUnits[id],dataViewOrganisationUnits[id],teiSearchOrganisationUnits[id],userRoles[id,authorities]&paging=false'
            ),
        onStep
    )) as { users: ExistingUser[] }
    if (found.users?.length === 1) {
        const u = found.users[0]
        const facilityOnly = (ous: Ref[] | undefined, mayBeEmpty: boolean) =>
            (mayBeEmpty && !ous?.length) ||
            (ous?.length === 1 && ous[0].id === facility)
        const auths = (u.userRoles ?? []).flatMap((x) => x.authorities ?? [])
        if (u.disabled) {
            throw new Error(
                'Its existing account is disabled: enable it first if the hub should have it back'
            )
        }
        if (
            !facilityOnly(u.organisationUnits, false) ||
            !facilityOnly(u.dataViewOrganisationUnits, true) ||
            !facilityOnly(u.teiSearchOrganisationUnits, true)
        ) {
            throw new Error(
                'Its existing account is not assigned to its facility only (capture, data view and search): correct it, then approve again'
            )
        }
        if (auths.length) {
            throw new Error(
                `Its existing account has authorities (${auths.join(', ')}): give it only the hub account role, then approve again`
            )
        }
        await step(
            'Giving its existing account a new password',
            () =>
                api.request(
                    'PATCH',
                    `/users/${u.id}`,
                    [
                        {
                            op: 'replace',
                            path: '/password',
                            value: creds.password,
                        },
                        // The group in the user, not through the group: DHIS2 lets hub
                        // managers add members that way (F_USER_GROUPS_READ_ONLY_ADD_MEMBERS).
                        {
                            op: 'add',
                            path: '/userGroups',
                            value: [{ id: group }],
                        },
                    ],
                    'application/json-patch+json'
                ),
            onStep
        )
        return u.id
    }
    const uid = `H${token(10)}` // a DHIS2 uid starts with a letter
    const ou = [{ id: facility }]
    await step(
        'Creating its account',
        () =>
            api.request('POST', '/users', {
                id: uid,
                username: creds.username,
                password: creds.password,
                firstName: 'Facility hub',
                surname: creds.username,
                organisationUnits: ou,
                dataViewOrganisationUnits: ou,
                teiSearchOrganisationUnits: ou,
                userRoles: [{ id: role }],
                userGroups: [{ id: group }],
            }),
        onStep
    )
    return uid
}

/**
 * Dismisses a hub's request for an account (an old one, of a hub that no longer
 * exists): only accountRequested changes. A hub still running asks again by itself at
 * its next refresh (internal/core refreshAccount: "no request in the registry").
 */
export const dismiss = async (api: Api, hubID: string): Promise<void> => {
    const regPath = `/dataStore/facility-hub-registry/${encodeURIComponent(hubID)}`
    const entry = (await api.request('GET', regPath)) as Record<string, unknown>
    if (entry.accountRequested !== true || entry.account != null) {
        throw new Error('Not waiting for an account')
    }
    await api.request('PUT', regPath, { ...entry, accountRequested: false })
}
