import { Api, ApiError, getOrNull } from '@/hubaccount/api'

type Meta = {
    id: string
    createdBy?: { id?: string }
    sharing?: { owner?: string; public?: string }
}

/**
 * Writes facility-hub-admin/<key> (or <namespace>/<key>: facility-hub-config, the
 * country's settings, is written the same way), which must be writable by administrators only
 * (docs/DEPLOYMENT.md, section 6: a planned move there moves every hub). A new
 * dataStore entry is public (read and write) until its sharing is set, and its owner
 * (who created it) keeps write access whatever the sharing says. So an entry created by
 * someone else is deleted and created again; it is created empty, shared as public read
 * only (and read-write for the hub managers), and only then written.
 */
export const writeAdminEntry = async (
    api: Api,
    {
        namespace = 'facility-hub-admin',
        key,
        value,
        managers,
    }: {
        namespace?: 'facility-hub-admin' | 'facility-hub-config'
        key: string
        value: unknown
        managers?: string
    }
) => {
    const path = `/dataStore/${namespace}/${encodeURIComponent(key)}`
    const metaOf = async () =>
        (await api.request('GET', `${path}/metaData`)) as Meta
    try {
        await api.request('POST', path, {})
    } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) {
            throw e
        }
        const [meta, me] = await Promise.all([
            metaOf(),
            api.request('GET', '/me?fields=id') as Promise<{ id: string }>,
        ])
        if ((meta.sharing?.owner ?? meta.createdBy?.id) !== me.id) {
            await api.request('DELETE', path)
            await api.request('POST', path, {})
        }
    }
    await share(api, (await metaOf()).id, managers)
    await api.request('PUT', path, value)
}

const share = (api: Api, id: string, managers?: string) =>
    api.request(
        'POST',
        `/sharing?type=dataStore&id=${encodeURIComponent(id)}`,
        {
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                // Writable by the hub managers too.
                userGroupAccesses: managers
                    ? [{ id: managers, access: 'rw------' }]
                    : [],
            },
        }
    )

const adminPath = (key: string) => `/dataStore/facility-hub-admin/${key}`

const metaOrNull = (api: Api, key: string) =>
    getOrNull<Meta>(api, `${adminPath(key)}/metaData`)

// The facility-hub-admin entries the hubs trust, each with a value they read as "not
// set". accounts is writable by administrators only.
const empty: Record<string, unknown> = {
    accounts: {},
    'central-move': {},
    release: { version: '', pilotHubs: [], percent: 0 },
    retries: { retries: [] },
}

/**
 * Creates the facility-hub-admin entries that do not exist yet, protected as
 * writeAdminEntry protects them: on 2.42 any user may create a missing key, own it and
 * keep writing it while its public access is read only, which the hubs would trust.
 * Run when a hub manager opens the app. It returns the keys it created.
 */
export const ensureAdminEntries = async (api: Api, managers?: string) => {
    const made: string[] = []
    for (const key of Object.keys(empty)) {
        if (!(await metaOrNull(api, key))) {
            await writeAdminEntry(api, {
                key,
                value: empty[key],
                managers: key === 'accounts' ? undefined : managers,
            })
            made.push(key)
        }
    }
    return made
}

/**
 * Shares the facility-hub-admin entries (but accounts) with the hub managers group, once
 * a superuser names it: entries made before are writable by administrators only. One
 * any user may write is left as it is: its value is not to be trusted unseen.
 */
export const shareAdminEntries = async (api: Api, managers: string) => {
    for (const key of Object.keys(empty).filter((k) => k !== 'accounts')) {
        const meta = await metaOrNull(api, key)
        if (meta && meta.sharing?.public?.[1] !== 'w') {
            await share(api, meta.id, managers)
        }
    }
}
