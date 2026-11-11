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
} from '@dhis2/ui'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import React, { useState } from 'react'
import { Link } from 'react-router-dom'
import styles from './Pages.module.css'
import { useApi } from '@/api/useApi'
import { ApproveDialog } from '@/components/hub/ApproveDialog'
import { ManagersOnly } from '@/components/hub/ManagersOnly'
import { dismiss } from '@/hubaccount/approve'
import { when } from '@/hubs/format'
import {
    AccountsSettings,
    HubRow,
    useAdminEntry,
    useHubs,
    place,
} from '@/hubs/useHubs'

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
            {!settings?.group && (
                <NoticeBox title={i18n.t('Hub accounts not set up')}>
                    {i18n.t(
                        'A superuser must name the hub accounts group and role in Settings before hubs can be approved.'
                    )}
                </NoticeBox>
            )}
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
