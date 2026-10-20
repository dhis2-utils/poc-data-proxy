import { writeAdminEntry } from './adminEntry'
import { Heartbeat } from './flags'
import { HubRow } from './useHubs'
import { Api, ApiError } from '@/hubaccount/api'

/** Refused saves of one kind, programme ("" for none) and error code on one hub. */
export type RefusedGroup = {
    kind: string
    program: string
    code: string
    count: number
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

/** A manager's request that hubs send a group's refused saves again. */
export type Retry = {
    id: string
    kind: string
    program: string
    code: string
    at: string
    by: string
}

export const keptRetries = 50

/** The list with r appended, the last 50 kept. */
export const withRetry = (list: Retry[], r: Retry): Retry[] =>
    [...list, r].slice(-keptRetries)

const retriesPath = '/dataStore/facility-hub-admin/retries'

/** The retries asked for so far (facility-hub-admin/retries), oldest first. */
export const readRetries = async (api: Api): Promise<Retry[]> => {
    try {
        const v = (await api.request('GET', retriesPath)) as {
            retries?: Retry[]
        } | null
        return Array.isArray(v?.retries) ? v.retries : []
    } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
            return []
        }
        throw e
    }
}

/**
 * Asks every hub to send a group's refused saves again, as their authors, at its next
 * sync: appended to facility-hub-admin/retries, written as an admin entry.
 */
export const resend = async (
    api: Api,
    group: { kind: string; program: string; code: string },
    {
        by,
        at,
        id,
        managers,
    }: { by: string; at: string; id: string; managers?: string }
): Promise<Retry[]> => {
    const retries = withRetry(await readRetries(api), {
        id,
        kind: group.kind,
        program: group.program,
        code: group.code,
        at,
        by,
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
