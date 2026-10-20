import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/useApi'

export type Named = { id: string; displayName: string }

/** Programmes, person types or data sets, by display name. */
export const useNamed = (
    resource: 'programs' | 'trackedEntityTypes' | 'dataSets'
) => {
    const api = useApi()
    const q = useQuery({
        queryKey: ['named', resource],
        queryFn: () =>
            api.request(
                'GET',
                `/${resource}?fields=id,displayName&paging=false&order=displayName:asc`
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
