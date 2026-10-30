import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/useApi'

export type Named = { id: string; displayName: string }

/** Programmes, person types, data sets or generated attributes, by display name. */
export const useNamed = (
    resource:
        | 'programs'
        | 'trackedEntityTypes'
        | 'dataSets'
        | 'trackedEntityAttributes'
) => {
    const api = useApi()
    const generated =
        resource === 'trackedEntityAttributes'
            ? '&filter=generated:eq:true'
            : ''
    const q = useQuery({
        queryKey: ['named', resource],
        queryFn: () =>
            api.request(
                'GET',
                `/${resource}?fields=id,displayName&paging=false&order=displayName:asc${generated}`
            ) as Promise<Record<string, Named[]>>,
        select: (r) => r[resource] ?? [],
        staleTime: 10 * 60e3,
    })
    return {
        list: q.data ?? [],
        isLoading: q.isLoading,
        error: q.error as Error | null,
    }
}
