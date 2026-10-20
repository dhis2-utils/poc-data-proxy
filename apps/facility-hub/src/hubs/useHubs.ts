import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo } from 'react'
import { writeAdminEntry } from './adminEntry'
import { Flag, flags, Heartbeat, uid, visible } from './flags'
import { Registry, retiredEntry } from './registry'
import { ReleaseChoice } from './stage'
import { listNamespace, useApi } from '@/api/useApi'
import { useMe } from '@/hooks/useMe'
import { ApiError } from '@/hubaccount/api'

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
    'time,lastPush,pending,pendingOutbox,stuckOutbox,issues,diskFreeBytes,usb,hubTokenOwner,hubTokenExpires,lastUpgrade,needsUpgrade,version'

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
    createdAt?: string
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
            let heartbeat: Heartbeat | undefined
            try {
                heartbeat = (await api.request(
                    'GET',
                    `/dataStore/facility-hub-status/${encodeURIComponent(id)}`
                )) as Heartbeat
            } catch (e) {
                if (!(e instanceof ApiError && e.status === 404)) {
                    throw e
                }
            }
            let issues: Issue[] = []
            try {
                const res = (await api.request(
                    'GET',
                    `/dataStore/facility-hub-issues?fields=.&filter=hub_id:eq:${encodeURIComponent(id)}&pageSize=200`
                )) as { entries?: { key: string; value: Omit<Issue, 'key'> }[] }
                issues = (res.entries ?? [])
                    .map((e) => ({ key: e.key, ...e.value }))
                    .sort((a, b) =>
                        (b.createdAt ?? '').localeCompare(a.createdAt ?? '')
                    )
            } catch (e) {
                if (!(e instanceof ApiError && e.status === 404)) {
                    throw e
                }
            }
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
}

export const getOrNull = async <T>(
    api: ReturnType<typeof useApi>,
    path: string
): Promise<T | null> => {
    try {
        return (await api.request('GET', path)) as T
    } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
            return null
        }
        throw e
    }
}

const accountsPath = '/dataStore/facility-hub-admin/accounts'

export const useAccountsSettings = () => {
    const api = useApi()
    const client = useQueryClient()
    const query = useQuery({
        queryKey: ['accountsSettings'],
        queryFn: () => getOrNull<AccountsSettings>(api, accountsPath),
    })
    const save = useMutation(
        // Written by superusers only: not shared with the hub managers.
        (s: AccountsSettings) =>
            writeAdminEntry(api, { key: 'accounts', value: s }),
        {
            onSuccess: () => client.invalidateQueries(['accountsSettings']),
        }
    )
    return {
        settings: query.data,
        isLoading: query.isLoading,
        error: query.error as Error | null,
        save,
    }
}

/** A planned move of central (facility-hub-admin/central-move), read by every hub. */
export type CentralMove = { url: string; from?: string }

const movePath = '/dataStore/facility-hub-admin/central-move'

export const useCentralMove = () => {
    const api = useApi()
    const client = useQueryClient()
    const { settings } = useAccountsSettings()
    const managers = settings?.managers
    const query = useQuery({
        queryKey: ['centralMove'],
        queryFn: () => getOrNull<CentralMove>(api, movePath),
    })
    const done = { onSuccess: () => client.invalidateQueries(['centralMove']) }
    const set = useMutation(
        (m: CentralMove) =>
            writeAdminEntry(api, {
                key: 'central-move',
                value: m,
                managers: managers,
            }),
        done
    )
    // Withdrawn by writing it empty (hubs ignore a move without an address): deleting it
    // would let anyone create it again.
    const clear = useMutation(
        () =>
            writeAdminEntry(api, {
                key: 'central-move',
                value: {},
                managers: managers,
            }),
        done
    )
    return {
        move: query.data,
        isLoading: query.isLoading,
        error: query.error as Error | null,
        set,
        clear,
    }
}

/** The country's choice of hub version (facility-hub-admin/release), read by every hub. */
const releasePath = '/dataStore/facility-hub-admin/release'

export const useRelease = () => {
    const api = useApi()
    const client = useQueryClient()
    const { settings } = useAccountsSettings()
    const query = useQuery({
        queryKey: ['release'],
        queryFn: () => getOrNull<ReleaseChoice>(api, releasePath),
    })
    const save = useMutation(
        (c: ReleaseChoice) =>
            writeAdminEntry(api, {
                key: 'release',
                value: c,
                managers: settings?.managers,
            }),
        { onSuccess: () => client.invalidateQueries(['release']) }
    )
    return {
        choice: query.data,
        isLoading: query.isLoading,
        error: query.error as Error | null,
        save,
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

/** A key of the country's settings for every hub (facility-hub-config), or null. */
export const useConfigEntry = <T>(
    key: 'retention' | 'aggregate' | 'tokens'
) => {
    const api = useApi()
    const client = useQueryClient()
    const { settings } = useAccountsSettings()
    const query = useQuery({
        queryKey: ['config', key],
        queryFn: () =>
            getOrNull<T>(api, `/dataStore/facility-hub-config/${key}`),
    })
    const save = useMutation(
        (value: T) =>
            writeAdminEntry(api, {
                namespace: 'facility-hub-config',
                key,
                value,
                managers: settings?.managers,
            }),
        { onSuccess: () => client.invalidateQueries(['config', key]) }
    )
    return {
        value: query.data,
        isLoading: query.isLoading,
        error: query.error as Error | null,
        save,
    }
}
