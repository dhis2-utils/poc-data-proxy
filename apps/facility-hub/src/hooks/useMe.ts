import { useApiDataQuery } from '@/utils/useApiDataQuery'

type MeResponse = {
    id: string
    username: string
    authorities: string[]
    organisationUnits: { id: string; path: string }[]
    userGroups?: { id: string }[]
}

export type Me = {
    id: string
    username: string
    /** Superusers (the ALL authority) see every hub, approve and move them, and set the app up. */
    superuser: boolean
    /** The paths of the user's org units: the user sees the hubs below them. */
    paths: string[]
    /** The user's groups: a member of the hub managers group approves and moves hubs. */
    groups: string[]
}

// Outside the hook, so its result keeps its identity between renders.
const toMe = (m: MeResponse): Me => ({
    id: m.id,
    username: m.username,
    superuser: m.authorities?.includes('ALL') ?? false,
    paths: (m.organisationUnits ?? []).map((o) => o.path),
    groups: (m.userGroups ?? []).map((g) => g.id),
})

export const useMe = () => {
    const {
        data: me,
        isLoading,
        error,
    } = useApiDataQuery<MeResponse, Error, Me>({
        queryKey: ['me'],
        query: {
            resource: 'me',
            params: {
                fields: 'id,username,authorities,organisationUnits[id,path],userGroups[id]',
            },
        },
        select: toMe,
        cacheTime: Infinity,
        staleTime: Infinity,
    })
    return { me, isLoading, error }
}
