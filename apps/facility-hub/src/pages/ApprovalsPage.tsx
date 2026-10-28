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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import React, { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import styles from './Pages.module.css'
import { useApi } from '@/api/useApi'
import { ApproveDialog } from '@/components/hub/ApproveDialog'
import { ManagersOnly } from '@/components/hub/ManagersOnly'
import { useCanManage } from '@/hooks/useCanManage'
import { dismiss } from '@/hubaccount/approve'
import { shareAdminEntries } from '@/hubs/adminEntry'
import { when } from '@/hubs/format'
import {
    AccountsSettings,
    HubRow,
    useAdminEntry,
    useHubs,
    place,
} from '@/hubs/useHubs'

type Named = { id: string; displayName: string }

// The hub accounts group and role, and the hub managers group, set once by a superuser
// (managers read them but do not change them).
const AccountsSettingsCard = () => {
    const api = useApi()
    const {
        value: settings,
        isLoading,
        save,
    } = useAdminEntry<AccountsSettings>('facility-hub-admin', 'accounts')
    const { superuser } = useCanManage()
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
    const [managers, setManagers] = useState('')
    const [owners, setOwners] = useState('')
    useEffect(() => {
        setGroup(settings?.group ?? '')
        setRole(settings?.role ?? '')
        setManagers(settings?.managers ?? '')
        setOwners(settings?.owners ?? '')
    }, [settings])

    if (isLoading || choices.isLoading) {
        return <CircularLoader small />
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Hub accounts')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Each approved hub gets a user in this group (write sharing on the facility-hub namespaces, except facility-hub-admin) with this role, which must have no authorities. Members of the hub managers group approve hubs and make move codes without being superusers; they need a role with F_USER_ADD, F_USER_VIEW, F_USER_GROUPS_READ_ONLY_ADD_MEMBERS and M_facilityhub, and read access to the hub accounts group. Each hub makes its registry entry writable by the hub owners group (staff who set hubs up, take them over or replace them), the hub accounts group and the hub managers only, once the hub owners group is named. Only superusers change these settings.'
                )}
            </div>
            <div className={styles.row}>
                <SingleSelectField
                    label={i18n.t('User group')}
                    selected={group}
                    onChange={({ selected }) => setGroup(selected)}
                    disabled={!superuser}
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
                    disabled={!superuser}
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
                <SingleSelectField
                    label={i18n.t('Hub managers group')}
                    selected={managers}
                    onChange={({ selected }) => setManagers(selected)}
                    disabled={!superuser}
                    filterable
                    clearable
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
                    label={i18n.t('Hub owners group')}
                    selected={owners}
                    onChange={({ selected }) => setOwners(selected)}
                    disabled={!superuser}
                    filterable
                    clearable
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
                <Button
                    onClick={() =>
                        save.mutate(
                            {
                                group,
                                role,
                                ...(managers && { managers }),
                                ...(owners && { owners }),
                            },
                            {
                                // Entries made before the managers were named.
                                onSuccess: () =>
                                    managers &&
                                    managers !== settings?.managers &&
                                    shareAdminEntries(api, managers),
                            }
                        )
                    }
                    loading={save.isLoading}
                    disabled={
                        !superuser ||
                        !group ||
                        !role ||
                        (group === settings?.group &&
                            role === settings?.role &&
                            managers === (settings?.managers ?? '') &&
                            owners === (settings?.owners ?? ''))
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
    const { value: settings } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
    const [approving, setApproving] = useState<HubRow | null>(null)
    const api = useApi()
    const client = useQueryClient()
    const dismissing = useMutation({
        mutationFn: (hubID: string) => dismiss(api, hubID),
        onSettled: () => client.invalidateQueries(['hubs']),
    })

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
                <div className={styles.muted}>
                    {i18n.t(
                        'Dismiss removes a request that will never be approved, such as an old one of a hub that no longer exists. A hub that is still running asks again by itself at its next refresh.'
                    )}
                </div>
                {dismissing.error && (
                    <NoticeBox error title={i18n.t('Not dismissed')}>
                        {(dismissing.error as Error).message}
                    </NoticeBox>
                )}
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
                                            disabled={!settings?.group}
                                            onClick={() => setApproving(h)}
                                        >
                                            {i18n.t('Approve')}
                                        </Button>{' '}
                                        <Button
                                            small
                                            secondary
                                            loading={
                                                dismissing.isLoading &&
                                                dismissing.variables === h.id
                                            }
                                            onClick={() =>
                                                dismissing.mutate(h.id)
                                            }
                                        >
                                            {i18n.t('Dismiss')}
                                        </Button>
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
            </div>
            {approving && settings?.group && (
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
    <ManagersOnly>
        <Approvals />
    </ManagersOnly>
)
