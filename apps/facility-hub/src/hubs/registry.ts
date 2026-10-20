import i18n from '@dhis2/d2-i18n'
import { Api, ApiError } from '@/hubaccount/api'

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

/** The entry of a hub whose tokens a manager revoked (every other field kept). */
export const revokedEntry = (
    r: Registry,
    by: string,
    at: string
): Registry => ({
    ...r,
    status: 'revoked',
    revokedBy: by,
    revokedAt: at,
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
}

/**
 * Revokes a hub's tokens (spec 2026-10-20, section 2): deletes each token its entry
 * lists, then marks the entry revoked, read again just before writing so nothing
 * written meanwhile is lost. Central answers 404 for a token that is gone or not
 * shared with the user: such tokens are counted, not fatal.
 */
export const revokeTokens = async (
    api: Api,
    hubId: string,
    { by, at }: { by: string; at: string }
): Promise<RevokeResult> => {
    const tokens = (await read(api, hubId)).tokens ?? []
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
    const entry = await read(api, hubId)
    await api.request('PUT', entryPath(hubId), revokedEntry(entry, by, at))
    return {
        listed: tokens.length,
        deleted: tokens.length - failed.length,
        failed,
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
