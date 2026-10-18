import i18n from '@dhis2/d2-i18n'
import { IconChevronLeft24 } from '@dhis2/ui'
import cx from 'classnames'
import React, { useState } from 'react'
import { NavLink } from 'react-router-dom'
import styles from './Sidebar.module.css'
import { Sidenav, SidenavItems, SidenavLink } from './sidenav'
import { useMe } from '@/hooks/useMe'

const SidebarNavLink = ({
    to,
    label,
    end,
}: {
    to: string
    label: string
    end?: boolean
}) => (
    <SidenavLink
        to={to}
        label={label}
        end={end}
        LinkComponent={
            NavLink as unknown as React.ComponentType<{
                to: string
                end?: boolean
            }>
        }
    />
)

export const Sidebar = ({
    className,
    hideSidebar,
}: {
    className?: string
    hideSidebar?: boolean
}) => {
    const [collapsed, setCollapsed] = useState(false)
    const isCollapsed = collapsed || hideSidebar
    const { me } = useMe()

    return (
        <aside
            className={cx(styles.asideWrapper, className, {
                [styles.collapsed]: isCollapsed,
            })}
        >
            <Sidenav>
                <SidenavItems>
                    <SidebarNavLink to="/" label={i18n.t('Hubs')} end />
                    {me?.superuser && (
                        <SidebarNavLink
                            to="/approvals"
                            label={i18n.t('Approvals')}
                        />
                    )}
                    {me?.superuser && (
                        <SidebarNavLink
                            to="/moves"
                            label={i18n.t('Central moves')}
                        />
                    )}
                </SidenavItems>
            </Sidenav>
            <button
                className={styles.collapseButton}
                type="button"
                onClick={() => setCollapsed(!collapsed)}
                aria-label={i18n.t('Collapse menu')}
            >
                <div
                    className={cx(styles.iconWrapper, {
                        [styles.collapsed]: isCollapsed,
                    })}
                >
                    <IconChevronLeft24 />
                </div>
            </button>
        </aside>
    )
}
