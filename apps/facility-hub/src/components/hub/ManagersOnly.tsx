import i18n from '@dhis2/d2-i18n'
import { CircularLoader, NoticeBox } from '@dhis2/ui'
import React, { PropsWithChildren } from 'react'
import { useCanManage } from '@/hooks/useCanManage'
import styles from '@/pages/Pages.module.css'

/** Shows its children to superusers and hub managers only. */
export const ManagersOnly = ({ children }: PropsWithChildren) => {
    const { canManage, isLoading, error } = useCanManage()
    if (isLoading) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    if (error) {
        return (
            <NoticeBox error title={i18n.t('Could not read your user')}>
                {error.message}
            </NoticeBox>
        )
    }
    if (!canManage) {
        return (
            <NoticeBox title={i18n.t('For hub managers')}>
                {i18n.t(
                    'Only hub managers and superusers approve hubs and give out move codes.'
                )}
            </NoticeBox>
        )
    }
    return <>{children}</>
}
