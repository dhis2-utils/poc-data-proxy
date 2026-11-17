import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo } from 'react'
import { writeAdminEntry } from './adminEntry'
import { Flag, flags, Heartbeat, uid, visible } from './flags'
import { Registry, retiredEntry } from './registry'
import { listNamespace, useApi } from '@/api/useApi'
import { useMe } from '@/hooks/useMe'
import { Api, getOrNull } from '@/hubaccount/api'

export type { Registry } from './registry'

export type OrgUnit = {
    id: string
    displayName: string
    path: string
    ancestors?: { displayName: string }[]
}

/** Where an org unit is, below the country: "Bo / Badjia". */
export const place = (o?: OrgUnit) =>
    (o?.ancestors ?? [])
        .slice(1)
        .map((a) => a.displayName)
        .join(' / ')

export type HubRow = {
    id: string
    registry: Registry
    heartbeat?: Heartbeat
    orgUnit?: OrgUnit
    flags: Flag[]
    /** Retired, or its tokens revoked: left out of rollouts and move codes. */
    retired: boolean
    waiting: boolean
}

// The heartbeat fields the list shows: not its 90-day history.
const listFields =
    'time,lastPush,oldestPending,pending,pendingOutbox,stuckOutbox,issues,diskFreeBytes,usb,hubTokenOwner,hubTokenExpires,lastUpgrade,needsUpgrade,version,refused,refusedGroups,unprotected,unsigned,retriesNotApplied'

const chunks = <T>(xs: T[], n: number) =>
    Array.from({ length: Math.ceil(xs.length / n) }, (_, i) =>
        xs.slice(i * n, i * n + n)
    )

/** Every hub the user may see, with its latest heartbeat, facility and flags. */
export const useHubs = () => {
    const api = useApi()
    const { me, isLoading: meLoading, error: meError } = useMe()
    const query = useQuery({
        queryKey: ['hubs'],
        enabled: !!me,
        staleTime: 60e3,
        queryFn: async () => {
            const [registry, status] = await Promise.all([
                listNamespace<Registry>(api, 'facility-hub-registry', '.'),
                listNamespace<Heartbeat>(
                    api,
                    'facility-hub-status',
                    listFields
                ),
            ])
            const beats = new Map(status.map((s) => [s.key, s.value]))
            const ids = [
                ...new Set(
                    registry
                        .map((r) => r.value.facility)
                        .filter((f): f is string => !!f && uid(f))
                ),
            ]
            const ous = new Map<string, OrgUnit>()
            for (const part of chunks(ids, 100)) {
                const res = (await api.request(
                    'GET',
                    `/organisationUnits?filter=id:in:[${part.join(',')}]&fields=id,displayName,path,ancestors[displayName]&paging=false`
                )) as { organisationUnits: OrgUnit[] }
                res.organisationUnits.forEach((o) => ous.set(o.id, o))
            }
            const now = new Date()
            return registry.map(({ key, value }): HubRow => {
                const heartbeat = beats.get(key)
                const retired = retiredEntry(value)
                return {
                    id: key,
                    registry: value,
                    heartbeat,
                    orgUnit: value.facility
                        ? ous.get(value.facility)
                        : undefined,
                    flags: retired ? [] : flags(heartbeat, now),
                    retired,
                    waiting:
                        value.accountRequested === true &&
                        value.account == null &&
                        !retired,
                }
            })
        },
    })
    const hubs = useMemo(
        () =>
            me && query.data
                ? query.data.filter((h) => visible(h.orgUnit?.path ?? '', me))
                : undefined,
        [me, query.data]
    )
    return {
        hubs,
        me,
        isLoading: meLoading || query.isInitialLoading,
        error: meError ?? (query.error as Error | null),
    }
}

/** One refused change, as the hub reports it (facility-hub-issues/<hub ID>-<n>). */
export type Issue = {
    key: string
    kind?: string
    objectUid?: string
    /** The author's DHIS2 username (hubs from this version on). */
    username?: string
    /** An enrollment's or event's person; "" for an event programme's event. */
    trackedEntity?: string
    createdAt?: string
    /**
     * Central changed the record after the refusal: the hub shows central's version,
     * and the change waits for a manager to resend or discard it.
     */
    superseded?: boolean
    error?: {
        message?: string
        errorReports?: { errorCode?: string; message?: string }[]
    }
}

/** A hub's full heartbeat (with its history) and its refused changes. */
export const useHub = (id: string) => {
    const api = useApi()
    return useQuery({
        queryKey: ['hub', id],
        staleTime: 60e3,
        queryFn: async () => {
            const heartbeat =
                (await getOrNull<Heartbeat>(
                    api,
                    `/dataStore/facility-hub-status/${encodeURIComponent(id)}`
                )) ?? undefined
            const res = await getOrNull<{
                entries?: { key: string; value: Omit<Issue, 'key'> }[]
            }>(
                api,
                `/dataStore/facility-hub-issues?fields=.&filter=hub_id:eq:${encodeURIComponent(id)}&pageSize=200`
            )
            const issues: Issue[] = (res?.entries ?? [])
                .map((e) => ({ key: e.key, ...e.value }))
                .sort((a, b) =>
                    (b.createdAt ?? '').localeCompare(a.createdAt ?? '')
                )
            return { heartbeat, issues }
        },
    })
}

/** The hub accounts user group and role an approval uses (facility-hub-admin/accounts). */
export type AccountsSettings = {
    group: string
    role: string
    /** The hub managers group: its members approve hubs and make move codes. */
    managers?: string
    /** The hub owners group: staff who set hubs up; each hub's registry entry is writable by it. */
    owners?: string
}

/**
 * The accounts settings to write: the chosen ones, except that a hub managers or hub
 * owners group once named stays. Central shares much with each of them, once (move keys,
 * settings, tokens; registry entries), and none of it would follow a new group: the old
 * one would keep it and the new one would not get it. Who is in the group changes in
 * the Users app instead.
 */
export const accountsValue = (
    chosen: AccountsSettings,
    latest?: AccountsSettings | null
): AccountsSettings => {
    const managers = latest?.managers || chosen.managers
    const owners = latest?.owners || chosen.owners
    return {
        ...latest,
        group: chosen.group,
        role: chosen.role,
        ...(managers && { managers }),
        ...(owners && { owners }),
    }
}

/** A planned move of central (facility-hub-admin/central-move), read by every hub. */
export type CentralMove = { url: string; from?: string }

type AdminNamespace = 'facility-hub-admin' | 'facility-hub-config'

const adminEntryQuery = <T>(
    api: Api,
    namespace: AdminNamespace,
    key: string
) => ({
    queryKey: ['adminEntry', namespace, key],
    queryFn: () =>
        getOrNull<T>(api, `/dataStore/${namespace}/${encodeURIComponent(key)}`),
})

/**
 * An entry of facility-hub-admin (accounts, central-move, release) or facility-hub-config
 * (the country's settings for every hub: retention, aggregate, tokens): its value, or
 * null if it is not set; save, which writes it as writeAdminEntry does; and update, which
 * writes a change to the entry as it is now, then runs afterWrite(written, latest) before
 * the entry is read again. Every entry
 * but accounts is writable by the hub managers group that accounts names; accounts
 * itself is written by superusers only.
 */
export const useAdminEntry = <T>(
    namespace: AdminNamespace,
    key: string,
    afterWrite?: (written: T, latest: T | null) => Promise<void>
) => {
    const api = useApi()
    const client = useQueryClient()
    const entry = adminEntryQuery<T>(api, namespace, key)
    const query = useQuery(entry)
    const accounts = useQuery(
        adminEntryQuery<AccountsSettings>(api, 'facility-hub-admin', 'accounts')
    )
    const shared = !(namespace === 'facility-hub-admin' && key === 'accounts')
    const save = useMutation(
        (value: T) =>
            writeAdminEntry(api, {
                namespace,
                key,
                value,
                managers: shared ? accounts.data?.managers : undefined,
            }),
        { onSuccess: () => client.invalidateQueries(entry.queryKey) }
    )
    // Writes change(the entry as central has it now), so that a write based on what
    // the page showed does not undo another manager's change made since. change may
    // throw to refuse; the entry is read again either way.
    const update = useMutation(
        async (change: (latest: T | null) => T) => {
            const latest = await entry.queryFn()
            const value = change(latest)
            await writeAdminEntry(api, {
                namespace,
                key,
                value,
                managers: shared ? accounts.data?.managers : undefined,
            })
            // Inside the mutation, so that the page shows the entry as saved only once
            // this is done too (a person, or a test, leaving then does not cut it off).
            await afterWrite?.(value, latest)
        },
        { onSettled: () => client.invalidateQueries(entry.queryKey) }
    )
    return {
        value: query.data,
        isLoading: query.isLoading,
        error: query.error as Error | null,
        save,
        update,
    }
}

/** Every hub's move key, by hub ID (administrators only; never shown). */
export const useMoveKeys = (enabled: boolean) => {
    const api = useApi()
    return useQuery({
        queryKey: ['moveKeys'],
        enabled,
        staleTime: 0,
        cacheTime: 0,
        queryFn: async () =>
            new Map(
                (
                    await listNamespace<{ moveKey?: string }>(
                        api,
                        'facility-hub-keys',
                        '.'
                    )
                ).map((k) => [k.key, k.value.moveKey])
            ),
    })
}
