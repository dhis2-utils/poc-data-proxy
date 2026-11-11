import { Api, ApiError, getOrNull } from '@/hubaccount/api'
import { fromBase64, instructionCode } from '@/hubaccount/codes'

type Meta = {
    id: string
    createdBy?: { id?: string }
    sharing?: { owner?: string; public?: string }
}

/**
 * The move keys the user can read (hub managers and superusers), by hub ID: one for
 * each approved hub.
 */
export const readMoveKeys = async (api: Api) => {
    const res = await getOrNull<{
        entries?: { key: string; value?: { moveKey?: string } }[]
    }>(api, '/dataStore/facility-hub-keys?fields=.&paging=false')
    const keys = new Map<string, Uint8Array>()
    for (const e of res?.entries ?? []) {
        const k = fromBase64(e.value?.moveKey ?? '')
        if (k.length === 32) {
            keys.set(e.key, k)
        }
    }
    return keys
}

/**
 * value as the hubs take it from the entry kind ("<namespace>/<key>"): with the time
 * it is issued and a code for each hub in keys, made with that hub's move key
 * (docs/superpowers/specs/2026-11-11-signed-instructions.md). A hub acts on an entry
 * only with a valid code for itself, no older than the last one it took.
 */
export const signedValue = (
    value: unknown,
    kind: string,
    keys: Map<string, Uint8Array>
) => {
    const issued = new Date().toISOString()
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { codes, ...rest } = (value ?? {}) as Record<string, unknown>
    const obj = { ...rest, issued }
    return {
        ...obj,
        codes: Object.fromEntries(
            [...keys].map(([hub, key]) => [
                hub,
                instructionCode(key, hub, { kind, obj }),
            ])
        ),
    }
}

/**
 * Writes facility-hub-admin/<key> (or <namespace>/<key>: facility-hub-config, the
 * country's settings, is written the same way), which must be writable by administrators only
 * (docs/DEPLOYMENT.md, section 6: a planned move there moves every hub). A new
 * dataStore entry is public (read and write) until its sharing is set, and its owner
 * (who created it) keeps write access whatever the sharing says. So an entry created by
 * someone else is deleted and created again; it is created empty, shared as public read
 * only (and read-write for the hub managers), and only then written. The value is
 * signed for every approved hub (signedValue), unless sign is false.
 */
export const writeAdminEntry = async (
    api: Api,
    {
        namespace = 'facility-hub-admin',
        key,
        value,
        managers,
        sign = true,
    }: {
        namespace?: 'facility-hub-admin' | 'facility-hub-config'
        key: string
        value: unknown
        managers?: string
        sign?: boolean
    }
) => {
    const path = `/dataStore/${namespace}/${encodeURIComponent(key)}`
    const body = sign
        ? signedValue(value, `${namespace}/${key}`, await readMoveKeys(api))
        : value
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
    await api.request('PUT', path, body)
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

/** The country's settings (facility-hub-config) the hubs read. */
export const configKeys = [
    'retention',
    'aggregate',
    'tokens',
    'reserved',
    'localReads',
    'metadataReload',
]

/**
 * Creates the facility-hub-admin entries that do not exist yet, protected as
 * writeAdminEntry protects them, with values the hubs read as not set (unsigned:
 * nothing to act on): on 2.42 any user may create a missing key, own it and keep
 * writing it while its public access is read only, and a hub not yet approved reads
 * accounts and release as they are. The settings (facility-hub-config) need no such
 * entry: a hub not yet approved uses the defaults, an approved one takes them signed.
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
                sign: false,
            })
            made.push(key)
        }
    }
    return made
}

/**
 * Adds an approved hub's code to each entry the app signed before (one with a valid
 * code for another hub), with its content and issued time unchanged, so the hub takes
 * what is in force without waiting for the next save. An entry signed by no one is
 * left: its author cannot be told. accounts is left when the user may not write it
 * (only superusers do): the hub gets its code at the next save by a superuser.
 */
export const addHubCodes = async (api: Api, hubID: string) => {
    const keys = await readMoveKeys(api)
    const key = keys.get(hubID)
    if (!key) {
        return
    }
    const kinds = [
        ...Object.keys(empty).map((k) => `facility-hub-admin/${k}`),
        ...configKeys.map((k) => `facility-hub-config/${k}`),
    ]
    for (const kind of kinds) {
        const path = `/dataStore/${kind}`
        const v = await getOrNull<Record<string, unknown>>(api, path)
        const codes = (v?.codes ?? {}) as Record<string, string>
        const vouched =
            !!v &&
            Object.entries(codes).some(
                ([hub, code]) =>
                    hub !== hubID &&
                    keys.has(hub) &&
                    instructionCode(keys.get(hub)!, hub, { kind, obj: v }) ===
                        code
            )
        if (!vouched) {
            continue
        }
        try {
            await api.request('PUT', path, {
                ...v,
                codes: {
                    ...codes,
                    [hubID]: instructionCode(key, hubID, { kind, obj: v }),
                },
            })
        } catch (e) {
            if (!(e instanceof ApiError && e.status === 403)) {
                throw e
            }
        }
    }
}

/**
 * Shares the facility-hub-admin entries (but accounts) and the facility-hub-config ones
 * with the hub managers group, once a superuser names it: entries made before are
 * writable by administrators only. One any user may write is left as it is: its value
 * is not to be trusted unseen.
 */
export const shareAdminEntries = async (api: Api, managers: string) => {
    const paths = [
        ...Object.keys(empty)
            .filter((k) => k !== 'accounts')
            .map(adminPath),
        ...configKeys.map((k) => `/dataStore/facility-hub-config/${k}`),
    ]
    for (const path of paths) {
        const meta = await getOrNull<Meta>(api, `${path}/metaData`)
        if (meta && meta.sharing?.public?.[1] !== 'w') {
            await share(api, meta.id, managers)
        }
    }
}
