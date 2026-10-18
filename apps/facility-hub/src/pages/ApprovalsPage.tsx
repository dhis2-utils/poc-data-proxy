import i18n from '@dhis2/d2-i18n'
import {
    Button,
    CircularLoader,
    DataTable,
    DataTableBody,
    DataTableCell,
    DataTableColumnHeader,
    DataTableHead,
    DataTableRow,
    NoticeBox,
    SingleSelectField,
    SingleSelectOption,
} from '@dhis2/ui'
import { useQuery } from '@tanstack/react-query'
import React, { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import styles from './Pages.module.css'
import { useApi } from '@/api/useApi'
import { ApproveDialog } from '@/components/hub/ApproveDialog'
import { SuperuserOnly } from '@/components/hub/SuperuserOnly'
import { when } from '@/hubs/format'
import { HubRow, useAccountsSettings, useHubs, place } from '@/hubs/useHubs'

type Named = { id: string; displayName: string }

// The hub accounts group and role, chosen once by the national team.
const AccountsSettingsCard = () => {
    const api = useApi()
    const { settings, isLoading, save } = useAccountsSettings()
    const choices = useQuery({
        queryKey: ['groupsAndRoles'],
        queryFn: async () => {
            const [g, r] = (await Promise.all([
                api.request(
                    'GET',
                    '/userGroups?fields=id,displayName&paging=false&order=displayName'
                ),
                api.request(
                    'GET',
                    '/userRoles?fields=id,displayName&paging=false&order=displayName'
                ),
            ])) as [{ userGroups: Named[] }, { userRoles: Named[] }]
            return { groups: g.userGroups, roles: r.userRoles }
        },
    })
    const [group, setGroup] = useState('')
    const [role, setRole] = useState('')
    useEffect(() => {
        setGroup(settings?.group ?? '')
        setRole(settings?.role ?? '')
    }, [settings])

    if (isLoading || choices.isLoading) {
        return <CircularLoader small />
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Hub accounts')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Each approved hub gets a user in this group (write sharing on the facility-hub namespaces, except facility-hub-admin) with this role, which must have no authorities.'
                )}
            </div>
            <div className={styles.row}>
                <SingleSelectField
                    label={i18n.t('User group')}
                    selected={group}
                    onChange={({ selected }) => setGroup(selected)}
                    filterable
                    className={styles.search}
                >
                    {(choices.data?.groups ?? []).map((g) => (
                        <SingleSelectOption
                            key={g.id}
                            value={g.id}
                            label={g.displayName}
                        />
                    ))}
                </SingleSelectField>
                <SingleSelectField
                    label={i18n.t('User role')}
                    selected={role}
                    onChange={({ selected }) => setRole(selected)}
                    filterable
                    className={styles.search}
                >
                    {(choices.data?.roles ?? []).map((r) => (
                        <SingleSelectOption
                            key={r.id}
                            value={r.id}
                            label={r.displayName}
                        />
                    ))}
                </SingleSelectField>
                <Button
                    onClick={() => save.mutate({ group, role })}
                    loading={save.isLoading}
                    disabled={
                        !group ||
                        !role ||
                        (group === settings?.group && role === settings?.role)
                    }
                >
                    {i18n.t('Save')}
                </Button>
            </div>
            {save.error && (
                <NoticeBox error title={i18n.t('Not saved')}>
                    {(save.error as Error).message}
                </NoticeBox>
            )}
        </div>
    )
}

const Approvals = () => {
    const { hubs, isLoading, error } = useHubs()
    const { settings } = useAccountsSettings()
    const [approving, setApproving] = useState<HubRow | null>(null)

    if (isLoading) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    if (error) {
        return (
            <NoticeBox error title={i18n.t('Could not load the hubs')}>
                {error.message}
            </NoticeBox>
        )
    }
    const waiting = (hubs ?? []).filter((h) => h.waiting)
    return (
        <div className={styles.page}>
            <h1 className={styles.title}>{i18n.t('Approvals')}</h1>
            <AccountsSettingsCard />
            <div className={styles.card}>
                <h2 className={styles.subtitle}>
                    {i18n.t('Hubs waiting for their own account')}
                </h2>
                {waiting.length === 0 ? (
                    <div className={styles.muted}>{i18n.t('None.')}</div>
                ) : (
                    <DataTable>
                        <DataTableHead>
                            <DataTableRow>
                                <DataTableColumnHeader>
                                    {i18n.t('Facility')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Hub')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Set up by')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Set up')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader />
                            </DataTableRow>
                        </DataTableHead>
                        <DataTableBody>
                            {waiting.map((h) => (
                                <DataTableRow key={h.id}>
                                    <DataTableCell>
                                        <Link to={`/hubs/${h.id}`}>
                                            {h.orgUnit?.displayName ??
                                                h.registry.facilityName}
                                        </Link>
                                        <div className={styles.muted}>
                                            {place(h.orgUnit)}
                                        </div>
                                    </DataTableCell>
                                    <DataTableCell>{h.id}</DataTableCell>
                                    <DataTableCell>
                                        {h.registry.setupBy}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {when(h.registry.setupAt)}
                                    </DataTableCell>
                                    <DataTableCell>
                                        <Button
                                            small
                                            primary
                                            disabled={!settings}
                                            onClick={() => setApproving(h)}
                                        >
                                            {i18n.t('Approve')}
                                        </Button>
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
            </div>
            {approving && settings && (
                <ApproveDialog
                    hub={approving}
                    settings={settings}
                    onClose={() => setApproving(null)}
                />
            )}
        </div>
    )
}

export const ApprovalsPage = () => (
    <SuperuserOnly>
        <Approvals />
    </SuperuserOnly>
)
