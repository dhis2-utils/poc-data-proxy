import { useApiDataQuery } from '@/utils/useApiDataQuery'

export type Named = { id: string; displayName: string }

/** Programmes, person types or data sets, by display name. */
export const useNamed = (
    resource: 'programs' | 'trackedEntityTypes' | 'dataSets'
) => {
    const q = useApiDataQuery<Record<string, Named[]>, Error, Named[]>({
        queryKey: ['named', resource],
        query: {
            resource,
            params: {
                fields: 'id,displayName',
                paging: false,
                order: 'displayName:asc',
            },
        },
        select: (r) => r[resource] ?? [],
        staleTime: 10 * 60e3,
    })
    return { list: q.data ?? [], isLoading: q.isLoading, error: q.error }
}
