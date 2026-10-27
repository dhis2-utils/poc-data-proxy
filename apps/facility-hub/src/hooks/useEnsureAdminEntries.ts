import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCanManage } from './useCanManage'
import { useApi } from '@/api/useApi'
import { ensureAdminEntries } from '@/hubs/adminEntry'
import { AccountsSettings, useAdminEntry } from '@/hubs/useHubs'

/**
 * Creates the missing facility-hub-admin entries, protected, when a superuser or hub
 * manager opens the app (ensureAdminEntries). Once per visit; it returns the error
 * when one could not be created, for the layout to show.
 */
export const useEnsureAdminEntries = () => {
    const api = useApi()
    const client = useQueryClient()
    const { canManage, isLoading } = useCanManage()
    const { value: settings } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
    const { error } = useQuery({
        queryKey: ['ensureAdminEntries'],
        enabled: canManage && !isLoading,
        staleTime: Infinity,
        retry: false,
        queryFn: async () => {
            const made = await ensureAdminEntries(api, settings?.managers)
            if (made.length > 0) {
                client.invalidateQueries(['adminEntry'])
            }
            return made
        },
    })
    return { error }
}
