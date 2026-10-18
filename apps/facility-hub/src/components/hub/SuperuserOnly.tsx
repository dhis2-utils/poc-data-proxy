import i18n from '@dhis2/d2-i18n'
import { CircularLoader, NoticeBox } from '@dhis2/ui'
import React, { PropsWithChildren } from 'react'
import { useMe } from '@/hooks/useMe'
import styles from '@/pages/Pages.module.css'

/** Shows its children to superusers only (approving and moving need the ALL authority). */
export const SuperuserOnly = ({ children }: PropsWithChildren) => {
    const { me, isLoading } = useMe()
    if (isLoading) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    if (!me?.superuser) {
        return (
            <NoticeBox title={i18n.t('For the national team')}>
                {i18n.t(
                    'Only superusers approve hubs and give out move codes.'
                )}
            </NoticeBox>
        )
    }
    return <>{children}</>
}
