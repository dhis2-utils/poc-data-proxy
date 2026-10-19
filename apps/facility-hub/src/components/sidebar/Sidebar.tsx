import i18n from '@dhis2/d2-i18n'
import { IconChevronLeft24 } from '@dhis2/ui'
import cx from 'classnames'
import React, { useState } from 'react'
import { NavLink } from 'react-router-dom'
import styles from './Sidebar.module.css'
import { Sidenav, SidenavItems, SidenavLink } from './sidenav'
import { useCanManage } from '@/hooks/useCanManage'

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
    const { canManage } = useCanManage()

    return (
        <aside
            className={cx(styles.asideWrapper, className, {
                [styles.collapsed]: isCollapsed,
            })}
        >
            <Sidenav>
                <SidenavItems>
                    <SidebarNavLink to="/" label={i18n.t('Hubs')} end />
                    {canManage && (
                        <SidebarNavLink
                            to="/approvals"
                            label={i18n.t('Approvals')}
                        />
                    )}
                    {canManage && (
                        <SidebarNavLink
                            to="/moves"
                            label={i18n.t('Central moves')}
                        />
                    )}
                    {canManage && (
                        <SidebarNavLink
                            to="/version"
                            label={i18n.t('Hub version')}
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
