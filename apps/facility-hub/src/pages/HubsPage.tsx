import i18n from '@dhis2/d2-i18n'
import {
    CircularLoader,
    DataTable,
    DataTableBody,
    DataTableCell,
    DataTableColumnHeader,
    DataTableHead,
    DataTableRow,
    InputField,
    NoticeBox,
    SingleSelectField,
    SingleSelectOption,
    Switch,
} from '@dhis2/ui'
import React, { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import styles from './Pages.module.css'
import { FlagTags } from '@/components/hub/FlagTags'
import { useCanManage } from '@/hooks/useCanManage'
import { ago, gb } from '@/hubs/format'
import { HubRow, useHubs, place } from '@/hubs/useHubs'

const facilityName = (h: HubRow) =>
    h.orgUnit?.displayName ??
    h.registry.facilityName ??
    h.registry.facility ??
    ''

const usbText = (h: HubRow) => {
    const usb = h.heartbeat?.usb
    if (!usb) {
        return ''
    }
    if (!usb.present) {
        return i18n.t('missing')
    }
    return usb.writable && !usb.error ? i18n.t('working') : i18n.t('failing')
}

const retiredText = (h: HubRow) =>
    h.registry.status === 'revoked'
        ? i18n.t('tokens revoked')
        : i18n.t('retired')

const hubVersion = (h: HubRow) =>
    h.heartbeat?.version ?? h.registry.hubVersion ?? ''

export const HubsPage = () => {
    const { hubs, isLoading, error } = useHubs()
    const { canManage } = useCanManage()
    const [search, setSearch] = useState('')
    const [flaggedOnly, setFlaggedOnly] = useState(false)
    const [version, setVersion] = useState('')
    const navigate = useNavigate()

    const rows = useMemo(
        () =>
            (hubs ?? [])
                .filter((h) => !flaggedOnly || h.flags.length > 0)
                .filter((h) => !version || hubVersion(h) === version)
                .filter((h) =>
                    facilityName(h)
                        .toLowerCase()
                        .includes(search.trim().toLowerCase())
                )
                .sort(
                    (a, b) =>
                        Number(a.retired) - Number(b.retired) ||
                        b.flags.length - a.flags.length ||
                        facilityName(a).localeCompare(facilityName(b))
                ),
        [hubs, search, flaggedOnly, version]
    )
    const versions = useMemo(
        () =>
            [...new Set((hubs ?? []).map(hubVersion).filter((v) => v))].sort(),
        [hubs]
    )

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
    const waiting = (hubs ?? []).filter((h) => h.waiting).length

    return (
        <div className={styles.page}>
            <h1 className={styles.title}>{i18n.t('Facility hubs')}</h1>
            {canManage && waiting > 0 && (
                <NoticeBox title={i18n.t('Hubs waiting for approval')}>
                    {waiting === 1
                        ? i18n.t('1 hub waits for its own account.')
                        : i18n.t('{{- n}} hubs wait for their own account.', {
                              n: waiting,
                          })}{' '}
                    <Link to="/approvals">{i18n.t('Approve hubs')}</Link>
                </NoticeBox>
            )}
            <div className={styles.controls}>
                <InputField
                    className={styles.search}
                    placeholder={i18n.t('Search by facility')}
                    value={search}
                    onChange={({ value }) => setSearch(value ?? '')}
                />
                <SingleSelectField
                    className={styles.versionFilter}
                    placeholder={i18n.t('Any version')}
                    selected={version}
                    clearable
                    onChange={({ selected }) => setVersion(selected ?? '')}
                >
                    {versions.map((v) => (
                        <SingleSelectOption key={v} value={v} label={v} />
                    ))}
                </SingleSelectField>
                <Switch
                    label={i18n.t('Flagged only')}
                    checked={flaggedOnly}
                    onChange={({ checked }) => setFlaggedOnly(checked)}
                />
            </div>
            {rows.length === 0 ? (
                <NoticeBox title={i18n.t('No hubs')}>
                    {hubs?.length
                        ? i18n.t('No hub matches.')
                        : i18n.t(
                              'No hub has been set up in your org units yet.'
                          )}
                </NoticeBox>
            ) : (
                <DataTable>
                    <DataTableHead>
                        <DataTableRow>
                            <DataTableColumnHeader>
                                {i18n.t('Facility')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Last heartbeat')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Pending')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Refused')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Free disk')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('USB stick')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Version')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Token of')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Flags')}
                            </DataTableColumnHeader>
                        </DataTableRow>
                    </DataTableHead>
                    <DataTableBody>
                        {rows.map((h) => (
                            <DataTableRow
                                key={h.id}
                                className={
                                    h.retired ? styles.retired : undefined
                                }
                            >
                                <DataTableCell
                                    onClick={() => navigate(`/hubs/${h.id}`)}
                                    className={styles.link}
                                >
                                    <div data-hub={h.id}>{facilityName(h)}</div>
                                    <div className={styles.muted}>
                                        {place(h.orgUnit)}
                                        {h.retired
                                            ? ` · ${retiredText(h)}`
                                            : ''}
                                    </div>
                                </DataTableCell>
                                <DataTableCell>
                                    {h.heartbeat ? ago(h.heartbeat.time) : '—'}
                                </DataTableCell>
                                <DataTableCell>
                                    {h.heartbeat?.pendingOutbox ?? ''}
                                </DataTableCell>
                                <DataTableCell>
                                    {h.heartbeat?.issues ?? ''}
                                </DataTableCell>
                                <DataTableCell>
                                    {gb(h.heartbeat?.diskFreeBytes)}
                                </DataTableCell>
                                <DataTableCell>{usbText(h)}</DataTableCell>
                                <DataTableCell>
                                    {h.heartbeat?.version ??
                                        h.registry.hubVersion ??
                                        ''}
                                </DataTableCell>
                                <DataTableCell>
                                    {h.heartbeat?.hubTokenOwner ?? ''}
                                </DataTableCell>
                                <DataTableCell>
                                    <FlagTags flags={h.flags} />
                                </DataTableCell>
                            </DataTableRow>
                        ))}
                    </DataTableBody>
                </DataTable>
            )}
        </div>
    )
}
