import i18n from '@dhis2/d2-i18n'
import { signedValue } from './adminEntry'
import { Api, ApiError, getOrNull } from '@/hubaccount/api'
import { fromBase64, newPassword } from '@/hubaccount/codes'

/** A hub's registry entry (facility-hub-registry/<hub ID>, written at set-up). */
export type Registry = {
    hubId?: string
    facility?: string
    facilityName?: string
    /** active, retired or revoked (a missing status is active). */
    status?: string
    setupBy?: string
    setupAt?: string
    hubVersion?: string
    hubTokenOwner?: string
    accountRequested?: boolean
    account?: unknown
    boxKey?: string
    /** The IDs of the hub's live tokens, shared with the hub managers: never secrets. */
    tokens?: string[]
    revokedBy?: string
    revokedAt?: string
    /** The status the hub acts on, signed with its move key (approved hubs only). */
    signedStatus?: Record<string, unknown>
    /** The hub that replaced this one, or "reset" (reset on the hub) or "undo". */
    retiredBy?: string
    retiredByUser?: string
    retiredAt?: string
    /** The hubs this one replaced at its set-up. */
    replaces?: string[]
    /** A replacement of this hub that a manager undid. */
    undone?: { by: string; at: string; retiredBy: string }
    [field: string]: unknown
}

/** Whether a hub no longer works for its facility: retired, or its tokens revoked. */
export const retiredEntry = (r: Registry) =>
    r.status === 'retired' || r.status === 'revoked'

/** The hub that replaced this one, if it was retired by a replacement. */
export const replacedBy = (r: Registry): string | undefined =>
    r.status === 'retired' &&
    !!r.retiredBy &&
    r.retiredBy !== 'reset' &&
    r.retiredBy !== 'undo'
        ? r.retiredBy
        : undefined

/**
 * The entry of a hub whose tokens a manager revoked (every other field kept). An
 * approved hub acts only on signedStatus, signed with its move key (review R-23); the
 * plain status is for the app and for hubs without one.
 */
export const revokedEntry = (
    r: Registry,
    { by, at, moveKey }: { by: string; at: string; moveKey?: Uint8Array }
): Registry => ({
    ...r,
    status: 'revoked',
    revokedBy: by,
    revokedAt: at,
    ...(r.hubId &&
        moveKey && {
            signedStatus: signedValue(
                { status: 'revoked', by, at },
                `facility-hub-registry/${r.hubId}`,
                new Map([[r.hubId, moveKey]])
            ),
        }),
})

/**
 * Both entries after undoing a replacement (spec 2026-10-20, section 3): the old hub
 * active again, with the replacement kept as history under undone; the replacing hub
 * retired by the undo.
 */
export const undoneEntries = (
    old: Registry,
    replacing: Registry,
    { by, at }: { by: string; at: string }
): { old: Registry; replacing: Registry } => {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { retiredBy, retiredByUser, retiredAt, ...rest } = old
    return {
        old: {
            ...rest,
            status: 'active',
            undone: { by, at, retiredBy: retiredBy ?? '' },
        },
        replacing: {
            ...replacing,
            status: 'retired',
            retiredBy: 'undo',
            retiredByUser: by,
            retiredAt: at,
        },
    }
}

const entryPath = (id: string) =>
    `/dataStore/facility-hub-registry/${encodeURIComponent(id)}`

const read = async (api: Api, id: string) =>
    (await api.request('GET', entryPath(id))) as Registry

export type RevokeResult = {
    /** Tokens the entry listed. */
    listed: number
    deleted: number
    /** Tokens central would not delete: already gone, or not shared with the managers. */
    failed: string[]
    /**
     * The hub's own DHIS2 user: disabled, given a new random password (disabling was
     * refused), none (the hub has no account of its own) or failed.
     */
    account: 'disabled' | 'password' | 'none' | 'failed'
}

/**
 * Revokes a hub's tokens (spec 2026-10-20, section 2): marks the entry revoked, signed
 * for the hub when the user can read its move key, then deletes each token it lists,
 * then disables the hub's own user, whose password the hub holds. Revoked first, so a
 * hub that syncs meanwhile still reads it with its own token and drops what it holds.
 * Central answers 404 for a token that is gone or not shared with the user: such
 * tokens are counted, not fatal. A hub manager may disable the users of their own org
 * units (checked on 2.42 and 2.43, 11 November 2026).
 */
export const revokeTokens = async (
    api: Api,
    hubId: string,
    { by, at }: { by: string; at: string }
): Promise<RevokeResult> => {
    const entry = await read(api, hubId)
    const key = await getOrNull<{ moveKey?: string }>(
        api,
        `/dataStore/facility-hub-keys/${encodeURIComponent(hubId)}`
    ).catch(() => null)
    const moveKey = fromBase64(key?.moveKey ?? '')
    await api.request(
        'PUT',
        entryPath(hubId),
        revokedEntry(
            { hubId, ...entry },
            { by, at, moveKey: moveKey.length === 32 ? moveKey : undefined }
        )
    )
    const tokens = entry.tokens ?? []
    const failed: string[] = []
    for (const t of tokens) {
        try {
            await api.request('DELETE', `/apiToken/${encodeURIComponent(t)}`)
        } catch (e) {
            if (!(e instanceof ApiError)) {
                throw e
            }
            failed.push(t)
        }
    }
    return {
        listed: tokens.length,
        deleted: tokens.length - failed.length,
        failed,
        account: await disableAccount(api, hubId),
    }
}

// disableAccount disables the hub's own user (named as the hub), or gives it a new
// random password if disabling is refused.
const disableAccount = async (
    api: Api,
    hubId: string
): Promise<RevokeResult['account']> => {
    try {
        const found = (await api.request(
            'GET',
            `/users?filter=username:eq:${encodeURIComponent(hubId)}&fields=id&paging=false`
        )) as { users?: { id: string }[] }
        const id = found.users?.length === 1 ? found.users[0].id : null
        if (!id) {
            return 'none'
        }
        try {
            await api.request('POST', `/users/${id}/disabled`)
            return 'disabled'
        } catch (e) {
            if (!(e instanceof ApiError)) {
                throw e
            }
        }
        await api.request(
            'PATCH',
            `/users/${id}`,
            [{ op: 'replace', path: '/password', value: newPassword() }],
            'application/json-patch+json'
        )
        return 'password'
    } catch (e) {
        if (!(e instanceof ApiError)) {
            throw e
        }
        return 'failed'
    }
}

/**
 * Undoes a mistaken replacement (spec 2026-10-20, section 3). The replacing hub is
 * retired first: if that fails, the facility keeps one active hub.
 */
export const undoReplacement = async (
    api: Api,
    oldId: string,
    { by, at }: { by: string; at: string }
) => {
    const old = await read(api, oldId)
    const newId = replacedBy(old)
    if (!newId) {
        throw new Error(i18n.t('This hub was not retired by a replacement.'))
    }
    const replacing = await read(api, newId)
    if (retiredEntry(replacing)) {
        throw new Error(
            i18n.t(
                'The hub that replaced this one ({{- id}}) is no longer active, so there is nothing to undo here.',
                { id: newId }
            )
        )
    }
    const next = undoneEntries(old, replacing, { by, at })
    await api.request('PUT', entryPath(newId), next.replacing)
    await api.request('PUT', entryPath(oldId), next.old)
}
