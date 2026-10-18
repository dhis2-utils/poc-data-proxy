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
import React, { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import styles from './Pages.module.css'
import { ApproveDialog } from '@/components/hub/ApproveDialog'
import { FlagTags } from '@/components/hub/FlagTags'
import { flagSentence } from '@/hubs/flagText'
import { ago, gb, when } from '@/hubs/format'
import { useAccountsSettings, useHub, useHubs, place } from '@/hubs/useHubs'

export const HubPage = () => {
    const { id = '' } = useParams()
    const { hubs, me, isLoading, error } = useHubs()
    const detail = useHub(id)
    const { settings } = useAccountsSettings()
    const [approving, setApproving] = useState(false)

    if (isLoading || detail.isLoading) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    const err = error ?? (detail.error as Error | null)
    if (err) {
        return (
            <NoticeBox error title={i18n.t('Could not load the hub')}>
                {err.message}
            </NoticeBox>
        )
    }
    const hub = hubs?.find((h) => h.id === id)
    if (!hub) {
        return (
            <NoticeBox title={i18n.t('No such hub')}>
                {i18n.t(
                    'This hub does not exist, or is not in your org units.'
                )}{' '}
                <Link to="/">{i18n.t('All hubs')}</Link>
            </NoticeBox>
        )
    }
    const hb = detail.data?.heartbeat
    const history = [...(hb?.history ?? [])].reverse()
    const issues = detail.data?.issues ?? []
    const facility =
        hub.orgUnit?.displayName ??
        hub.registry.facilityName ??
        hub.registry.facility

    return (
        <div className={styles.page}>
            <div>
                <Link to="/">{i18n.t('All hubs')}</Link>
            </div>
            <h1 className={styles.title}>
                {facility}{' '}
                <span className={styles.muted}>{place(hub.orgUnit)}</span>
            </h1>
            <FlagTags flags={hub.flags} />
            {hub.flags.length > 0 && (
                <div className={styles.card}>
                    {hub.flags.map((f) => (
                        <div key={f.kind}>{flagSentence(f.kind, hb)}</div>
                    ))}
                </div>
            )}
            {me?.superuser && hub.waiting && (
                <NoticeBox title={i18n.t('Waiting for its own account')}>
                    <Button
                        primary
                        small
                        onClick={() => setApproving(true)}
                        disabled={!settings}
                    >
                        {i18n.t('Approve')}
                    </Button>{' '}
                    {!settings &&
                        i18n.t(
                            'Set the hub accounts group and role on the Approvals page first.'
                        )}
                </NoticeBox>
            )}
            <div className={styles.card}>
                <h2 className={styles.subtitle}>{i18n.t('Latest report')}</h2>
                <dl className={styles.facts}>
                    <dt>{i18n.t('Hub')}</dt>
                    <dd>{hub.id}</dd>
                    <dt>{i18n.t('Set up')}</dt>
                    <dd>
                        {when(hub.registry.setupAt)}{' '}
                        {hub.registry.setupBy &&
                            i18n.t('by {{- by}}', { by: hub.registry.setupBy })}
                    </dd>
                    <dt>{i18n.t('Last heartbeat')}</dt>
                    <dd>
                        {hb
                            ? `${when(hb.time)} (${ago(hb.time)})`
                            : i18n.t('never')}
                    </dd>
                    <dt>{i18n.t('Last sync')}</dt>
                    <dd>{when(hb?.lastSync)}</dd>
                    <dt>{i18n.t('Last error')}</dt>
                    <dd>{hb?.lastError || '—'}</dd>
                    <dt>{i18n.t('Pending saves')}</dt>
                    <dd>{hb?.pendingOutbox ?? '—'}</dd>
                    <dt>{i18n.t('Refused changes')}</dt>
                    <dd>{hb?.issues ?? '—'}</dd>
                    <dt>{i18n.t('Free disk')}</dt>
                    <dd>{gb(hb?.diskFreeBytes) || '—'}</dd>
                    <dt>{i18n.t('USB stick')}</dt>
                    <dd>
                        {hb?.usb
                            ? hb.usb.present
                                ? i18n.t('last copy {{- when}}', {
                                      when: ago(hb.usb.lastCopy),
                                  })
                                : i18n.t('missing')
                            : '—'}
                    </dd>
                    <dt>{i18n.t('Version')}</dt>
                    <dd>
                        {hb?.version ?? hub.registry.hubVersion}{' '}
                        {hb?.serverVersion &&
                            i18n.t('(central DHIS2 {{- v}})', {
                                v: hb.serverVersion,
                            })}
                    </dd>
                    <dt>{i18n.t('Housekeeping token')}</dt>
                    <dd>
                        {hb?.hubTokenOwner ?? '—'}{' '}
                        {hb?.hubTokenExpires &&
                            i18n.t('expires {{- when}}', {
                                when: when(hb.hubTokenExpires),
                            })}
                    </dd>
                </dl>
            </div>
            <div className={styles.card}>
                <h2 className={styles.subtitle}>{i18n.t('Last 90 days')}</h2>
                {history.length === 0 ? (
                    <div className={styles.muted}>
                        {i18n.t('No history reported yet.')}
                    </div>
                ) : (
                    <DataTable>
                        <DataTableHead>
                            <DataTableRow>
                                <DataTableColumnHeader>
                                    {i18n.t('Day')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Syncs')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Outages')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Minutes offline')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Longest outage (min)')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Pending peak')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Lowest free disk')}
                                </DataTableColumnHeader>
                            </DataTableRow>
                        </DataTableHead>
                        <DataTableBody>
                            {history.map((d) => (
                                <DataTableRow key={d.date}>
                                    <DataTableCell>{d.date}</DataTableCell>
                                    <DataTableCell>{d.syncs}</DataTableCell>
                                    <DataTableCell>{d.outages}</DataTableCell>
                                    <DataTableCell>
                                        {d.offlineMinutes}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {d.longestOfflineMinutes}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {d.pendingPeak}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {gb(d.diskFreeMinBytes)}
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
            </div>
            <div className={styles.card}>
                <h2 className={styles.subtitle}>{i18n.t('Refused changes')}</h2>
                <div className={styles.muted}>
                    {i18n.t(
                        'Values are masked (***) – central staff see which field caused it, not what was entered. The hub removes a change from this list once it is resolved.'
                    )}
                </div>
                {issues.length === 0 ? (
                    <div className={styles.muted}>{i18n.t('None.')}</div>
                ) : (
                    <DataTable>
                        <DataTableHead>
                            <DataTableRow>
                                <DataTableColumnHeader>
                                    {i18n.t('When')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Kind')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Object')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Error codes')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Message')}
                                </DataTableColumnHeader>
                            </DataTableRow>
                        </DataTableHead>
                        <DataTableBody>
                            {issues.map((is) => (
                                <DataTableRow key={is.key}>
                                    <DataTableCell>
                                        {when(is.createdAt)}
                                    </DataTableCell>
                                    <DataTableCell>{is.kind}</DataTableCell>
                                    <DataTableCell>
                                        {is.objectUid}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {[
                                            ...new Set(
                                                (is.error?.errorReports ?? [])
                                                    .map((r) => r.errorCode)
                                                    .filter(Boolean)
                                            ),
                                        ].join(', ')}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {is.error?.message}
                                        {(is.error?.errorReports ?? []).map(
                                            (r, i) => (
                                                <div
                                                    key={i}
                                                    className={styles.muted}
                                                >
                                                    {r.message}
                                                </div>
                                            )
                                        )}
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
            </div>
            {approving && settings && (
                <ApproveDialog
                    hub={hub}
                    settings={settings}
                    onClose={() => setApproving(false)}
                />
            )}
        </div>
    )
}
