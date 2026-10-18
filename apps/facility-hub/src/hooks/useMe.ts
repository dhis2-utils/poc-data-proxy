import { useApiDataQuery } from '@/utils/useApiDataQuery'

type MeResponse = {
    id: string
    username: string
    authorities: string[]
    organisationUnits: { id: string; path: string }[]
}

export type Me = {
    id: string
    username: string
    /** Superusers (the ALL authority) see every hub, and approve and move them. */
    superuser: boolean
    /** The paths of the user's org units: the user sees the hubs below them. */
    paths: string[]
}

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
                fields: 'id,username,authorities,organisationUnits[id,path]',
            },
        },
        select: (m) => ({
            id: m.id,
            username: m.username,
            superuser: m.authorities?.includes('ALL') ?? false,
            paths: (m.organisationUnits ?? []).map((o) => o.path),
        }),
        cacheTime: Infinity,
        staleTime: Infinity,
    })
    return { me, isLoading, error }
}
