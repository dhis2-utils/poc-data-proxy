import { useMe } from './useMe'
import { canManage } from '@/hubs/flags'
import { useAccountsSettings } from '@/hubs/useHubs'

/** Whether the user approves hubs and makes move codes: a superuser or a hub manager. */
export const useCanManage = () => {
    const { me, isLoading, error } = useMe()
    const settings = useAccountsSettings()
    return {
        canManage: !!me && canManage(me, settings.settings),
        superuser: !!me?.superuser,
        isLoading: isLoading || settings.isLoading,
        error: error ?? settings.error,
    }
}
