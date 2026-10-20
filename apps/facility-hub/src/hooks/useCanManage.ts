import { useMe } from './useMe'
import { canManage } from '@/hubs/flags'
import { AccountsSettings, useAdminEntry } from '@/hubs/useHubs'

/** Whether the user approves hubs and makes move codes: a superuser or a hub manager. */
export const useCanManage = () => {
    const { me, isLoading, error } = useMe()
    const settings = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
    return {
        canManage: !!me && canManage(me, settings.value),
        superuser: !!me?.superuser,
        isLoading: isLoading || settings.isLoading,
        error: error ?? settings.error,
    }
}
