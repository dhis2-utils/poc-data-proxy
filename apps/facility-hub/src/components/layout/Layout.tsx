import i18n from '@dhis2/d2-i18n'
import { NoticeBox } from '@dhis2/ui'
import React from 'react'
import { Outlet, useMatches } from 'react-router-dom'
import { Sidebar } from '../sidebar/Sidebar'
import styles from './Layout.module.css'
import { useEnsureAdminEntries } from '@/hooks/useEnsureAdminEntries'

export type RouteHandle = {
    fullWidth?: boolean
    /* whether to automatically collapse the sidebar when route is active */
    collapseSidebar?: boolean
}

export const Layout = () => {
    const { error } = useEnsureAdminEntries()
    const collapseSidebar = useMatches().some(
        (match) => (match.handle as RouteHandle)?.collapseSidebar
    )

    return (
        <div className={styles.wrapper}>
            <Sidebar className={styles.sidebar} hideSidebar={collapseSidebar} />
            <main className={styles.main}>
                {error ? (
                    <NoticeBox
                        warning
                        title={i18n.t('The hub settings could not be created')}
                    >
                        {i18n.t(
                            'Until they are, any user may create them, and hubs would trust what they write. Open the app again, or ask a superuser to.'
                        )}{' '}
                        {String(error)}
                    </NoticeBox>
                ) : null}
                <Outlet />
            </main>
        </div>
    )
}
