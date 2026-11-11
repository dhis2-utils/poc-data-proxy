import { writeAdminEntry } from './adminEntry'
import { Heartbeat } from './flags'
import { HubRow, Issue } from './useHubs'
import { Api, getOrNull } from '@/hubaccount/api'

/** Refused saves of one kind, programme ("" for none) and error code on one hub. */
export type RefusedGroup = {
    kind: string
    program: string
    code: string
    count: number
    /**
     * Of count, the saves whose record central changed after the refusal: the hub shows
     * central's version, and the save waits for Resend or Discard (review R-22).
     */
    superseded?: number
}

/** A group added up across hubs: its refused saves, and the hubs that have some. */
export type RefusalRow = RefusedGroup & { hubs: string[] }

/** A hub's groups: its heartbeat's refused list (at most 100 groups). */
export const refusedGroupsOf = (hb?: Heartbeat): RefusedGroup[] =>
    Array.isArray(hb?.refused) ? hb.refused : []

/** Hubs, not retired, that have more groups than their heartbeat lists. */
export const partlyReported = (hubs: HubRow[]): string[] =>
    hubs
        .filter(
            (h) =>
                !h.retired &&
                (h.heartbeat?.refusedGroups ?? 0) >
                    refusedGroupsOf(h.heartbeat).length
        )
        .map((h) => h.id)

/**
 * The refused saves of every hub still working (spec 2026-10-20, section 4), by kind,
 * programme and error code: most saves first.
 */
export const groupRefusals = (hubs: HubRow[]): RefusalRow[] => {
    const rows = new Map<string, RefusalRow>()
    for (const h of hubs) {
        if (h.retired) {
            continue
        }
        for (const g of refusedGroupsOf(h.heartbeat)) {
            const key = JSON.stringify([g.kind, g.program, g.code])
            const row = rows.get(key) ?? {
                kind: g.kind,
                program: g.program ?? '',
                code: g.code,
                count: 0,
                hubs: [],
            }
            row.count += g.count ?? 0
            if (g.superseded) {
                row.superseded = (row.superseded ?? 0) + g.superseded
            }
            if (!row.hubs.includes(h.id)) {
                row.hubs.push(h.id)
            }
            rows.set(key, row)
        }
    }
    return [...rows.values()].sort(
        (a, b) =>
            b.count - a.count ||
            a.kind.localeCompare(b.kind) ||
            a.program.localeCompare(b.program) ||
            a.code.localeCompare(b.code)
    )
}

/**
 * A manager's request that hubs send a group's refused saves again, or, with action
 * 'discard', that they throw them away.
 */
export type Retry = {
    id: string
    kind: string
    program: string
    code: string
    at: string
    by: string
    /** The hub user to send the saves as, instead of their authors. */
    user?: string
    /** 'discard' throws the saves away; absent (or 'resend') sends them again. */
    action?: 'resend' | 'discard'
    /** One refused change instead of a group: its key in facility-hub-issues. */
    issue?: string
    /** With issue: the hub and the object, for the list of requests. */
    hub?: string
    object?: string
    /**
     * For a group: the org units of the manager who asked. Only hubs below them take
     * it, and only for saves refused before at (review R-30a).
     */
    orgUnits?: string[]
}

/**
 * The org units a group instruction is for: the hubs the Refusals page counts. A
 * user's own org units; for a superuser, who sees every hub, the top of each hub's
 * path.
 */
export const scopeOf = (
    me: { superuser: boolean; paths: string[] },
    hubs: { orgUnit?: { path: string } }[]
): string[] =>
    [
        ...new Set(
            me.superuser
                ? hubs.map((h) => h.orgUnit?.path.split('/')[1] ?? '')
                : me.paths.map((p) => p.split('/').pop() ?? '')
        ),
    ].filter(Boolean)

export const isDiscard = (r: Retry) => r.action === 'discard'

/**
 * What a group instruction for orgUnits reaches, as far as this page can tell: the
 * group's refused saves and the hubs that report them, among the hubs below orgUnits
 * that still work, and how many of those hubs this page may not count in full (no
 * heartbeat, or more groups than their heartbeat lists). Each hub decides by its own
 * path (review R-30a).
 */
export const reachOf = (
    group: { kind: string; program: string; code: string },
    hubs: HubRow[],
    orgUnits: string[]
): { saves: number; hubs: number; uncounted: number } => {
    const inScope = hubs.filter(
        (h) =>
            !h.retired &&
            orgUnits.some((ou) =>
                (h.orgUnit?.path ?? '').split('/').includes(ou)
            )
    )
    let saves = 0
    let reporting = 0
    for (const h of inScope) {
        const n = refusedGroupsOf(h.heartbeat)
            .filter(
                (g) =>
                    g.kind === group.kind &&
                    (g.program ?? '') === group.program &&
                    g.code === group.code
            )
            .reduce((sum, g) => sum + (g.count ?? 0), 0)
        if (n > 0) {
            saves += n
            reporting++
        }
    }
    const uncounted = inScope.filter(
        (h) =>
            !h.heartbeat ||
            (h.heartbeat.refusedGroups ?? 0) >
                refusedGroupsOf(h.heartbeat).length
    ).length
    return { saves, hubs: reporting, uncounted }
}

/**
 * The error code of saves a hub refused itself: their author could not log in online
 * for 7 days after central stopped accepting their token. They can be resent as
 * another user.
 */
export const noLoginCode = 'NO_LOGIN'

/** The hubs that did not apply a retry at all, and why (their heartbeats). */
export const notAppliedOn = (
    hubs: HubRow[],
    id: string
): { hub: string; reason: string }[] =>
    hubs.flatMap((h) =>
        (h.heartbeat?.retriesNotApplied ?? [])
            .filter((r) => r.id === id)
            .map((r) => ({ hub: h.id, reason: r.reason }))
    )

export const keptRetries = 50

/** The list with r appended, the last 50 kept. */
export const withRetry = (list: Retry[], r: Retry): Retry[] =>
    [...list, r].slice(-keptRetries)

const retriesPath = '/dataStore/facility-hub-admin/retries'

/** The retries asked for so far (facility-hub-admin/retries), oldest first. */
export const readRetries = async (api: Api): Promise<Retry[]> => {
    const v = await getOrNull<{ retries?: Retry[] }>(api, retriesPath)
    return Array.isArray(v?.retries) ? v.retries : []
}

/**
 * Asks the hubs below orgUnits to send a group's refused saves again, as their
 * authors or as user, at their next sync: appended to facility-hub-admin/retries, written as an admin entry.
 */
export const resend = async (
    api: Api,
    group: { kind: string; program: string; code: string },
    {
        by,
        at,
        id,
        managers,
        user,
        orgUnits,
    }: {
        by: string
        at: string
        id: string
        managers?: string
        user?: string
        orgUnits: string[]
    }
): Promise<Retry[]> => {
    const as = user?.trim()
    const retries = withRetry(await readRetries(api), {
        id,
        kind: group.kind,
        program: group.program,
        code: group.code,
        at,
        by,
        ...(as ? { user: as } : {}),
        orgUnits,
    })
    await writeAdminEntry(api, {
        key: 'retries',
        value: { retries },
        managers,
    })
    return retries
}

/** A random identifier for a retry (hubs remember the ones they applied). */
export const retryId = () =>
    Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) =>
        b.toString(16).padStart(2, '0')
    ).join('')

/**
 * Asks the hubs below orgUnits to throw a group's refused saves away, with what they
 * blocked, at their next sync. A hub keeps central's version of each record, or a tombstone where central
 * has none.
 */
export const discardGroup = async (
    api: Api,
    group: { kind: string; program: string; code: string },
    {
        by,
        at,
        id,
        managers,
        orgUnits,
    }: {
        by: string
        at: string
        id: string
        managers?: string
        orgUnits: string[]
    }
): Promise<Retry[]> => {
    const retries = withRetry(await readRetries(api), {
        id,
        action: 'discard',
        kind: group.kind,
        program: group.program,
        code: group.code,
        at,
        by,
        orgUnits,
    })
    await writeAdminEntry(api, { key: 'retries', value: { retries }, managers })
    return retries
}

/** One refused change, as a hub page lists it (facility-hub-issues/<hub ID>-<n>). */
export type IssueRef = {
    key: string
    hub: string
    kind?: string
    objectUid?: string
    codes: string[]
}

/** Asks one hub to throw one refused change away, with what it blocked. */
export const discardChange = async (
    api: Api,
    issue: IssueRef,
    {
        by,
        at,
        id,
        managers,
    }: { by: string; at: string; id: string; managers?: string }
): Promise<Retry[]> => {
    const retries = withRetry(await readRetries(api), {
        id,
        action: 'discard',
        issue: issue.key,
        hub: issue.hub,
        object: issue.objectUid ?? '',
        kind: issue.kind ?? '',
        program: '',
        code: issue.codes.join(', '),
        at,
        by,
    })
    await writeAdminEntry(api, { key: 'retries', value: { retries }, managers })
    return retries
}

/** The discard asked for this refused change, if any. */
export const discardOf = (retries: Retry[], key: string): Retry | undefined =>
    retries.find((r) => isDiscard(r) && r.issue === key)

/** The distinct error codes of a refused change. */
export const issueCodes = (is: Issue): string[] => [
    ...new Set(
        (is.error?.errorReports ?? [])
            .map((r) => r.errorCode ?? '')
            .filter(Boolean)
    ),
]
