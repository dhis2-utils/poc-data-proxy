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
    Tag,
} from '@dhis2/ui'
import React, { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import styles from './Pages.module.css'
import { ApproveDialog } from '@/components/hub/ApproveDialog'
import { DiscardChange } from '@/components/hub/DiscardChange'
import { FlagTags } from '@/components/hub/FlagTags'
import { RevokeTokens, UndoReplacement } from '@/components/hub/HubActions'
import { HubHistory } from '@/components/hub/HubHistory'
import { useCanManage } from '@/hooks/useCanManage'
import { flagSentence } from '@/hubs/flagText'
import { ago, gb, when } from '@/hubs/format'
import { parseMiss } from '@/hubs/misses'
import { issueCodes } from '@/hubs/refusals'
import { saveTotals } from '@/hubs/saves'
import {
    AccountsSettings,
    useAdminEntry,
    useHub,
    useHubs,
    place,
} from '@/hubs/useHubs'

export const HubPage = () => {
    const { id = '' } = useParams()
    const { hubs, isLoading, error } = useHubs()
    const { canManage } = useCanManage()
    const detail = useHub(id)
    const { value: settings } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
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
    const saves = saveTotals(hb?.history)
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
            {hub.retired && (
                <span>
                    <Tag neutral>
                        {hub.registry.status === 'revoked'
                            ? i18n.t('Tokens revoked')
                            : i18n.t('Retired')}
                    </Tag>
                </span>
            )}
            <FlagTags flags={hub.flags} />
            {hub.flags.length > 0 && (
                <div className={styles.card}>
                    {hub.flags.map((f) => (
                        <div key={f.kind}>{flagSentence(f.kind, hb)}</div>
                    ))}
                </div>
            )}
            {canManage && hub.waiting && (
                <NoticeBox title={i18n.t('Waiting for its own account')}>
                    <Button
                        primary
                        small
                        onClick={() => setApproving(true)}
                        disabled={!settings?.group}
                    >
                        {i18n.t('Approve')}
                    </Button>{' '}
                    {!settings?.group &&
                        i18n.t(
                            'Set the hub accounts group and role on the Approvals page first.'
                        )}
                </NoticeBox>
            )}
            <HubHistory hub={hub} />
            {canManage && <UndoReplacement hub={hub} />}
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
                    <dt>{i18n.t('Saved online')}</dt>
                    <dd>
                        {i18n.t(
                            '{{- today}} today, {{- last30}} in the last 30 days',
                            {
                                today: saves.today.online,
                                last30: saves.last30.online,
                            }
                        )}
                    </dd>
                    <dt>{i18n.t('Saved offline, sent later')}</dt>
                    <dd>
                        {i18n.t(
                            '{{- today}} today, {{- last30}} in the last 30 days',
                            {
                                today: saves.today.offline,
                                last30: saves.last30.offline,
                            }
                        )}
                    </dd>
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
                    <dt>{i18n.t('Addresses')}</dt>
                    <dd>
                        {hb?.addresses?.length
                            ? hb.addresses.map((a) => `https://${a}`).join(', ')
                            : '—'}
                    </dd>
                    <dt>{i18n.t('Certificate fingerprint')}</dt>
                    <dd className={styles.mono}>{hb?.caFingerprint || '—'}</dd>
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
                                    {i18n.t('Saved online')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Saved offline, sent later')}
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
                                        {d.savedOnline ?? 0}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {d.savedOffline ?? 0}
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
                <h2 className={styles.subtitle}>
                    {i18n.t('Not available offline')}
                </h2>
                <div className={styles.muted}>
                    {i18n.t(
                        'What staff asked for while the hub was offline that it could not answer ("needs connection"), since the hub last started: mostly screens nobody had opened online. Each staff member opens Capture, and each programme\'s working list, once online.'
                    )}
                </div>
                {(hb?.offlineMissesTotal ?? 0) === 0 ? (
                    <div className={styles.muted}>{i18n.t('None.')}</div>
                ) : (
                    <>
                        <div className={styles.muted}>
                            {i18n.t('{{- total}} in all; the most frequent:', {
                                total: hb?.offlineMissesTotal,
                            })}
                        </div>
                        <DataTable>
                            <DataTableHead>
                                <DataTableRow>
                                    <DataTableColumnHeader>
                                        {i18n.t('Times')}
                                    </DataTableColumnHeader>
                                    <DataTableColumnHeader>
                                        {i18n.t('Request')}
                                    </DataTableColumnHeader>
                                    <DataTableColumnHeader>
                                        {i18n.t('Parameters')}
                                    </DataTableColumnHeader>
                                    <DataTableColumnHeader>
                                        {i18n.t('Client')}
                                    </DataTableColumnHeader>
                                </DataTableRow>
                            </DataTableHead>
                            <DataTableBody>
                                {(hb?.offlineMisses ?? []).map((m) => {
                                    const r = parseMiss(m.request)
                                    return (
                                        <DataTableRow key={m.request}>
                                            <DataTableCell>
                                                {m.count}
                                            </DataTableCell>
                                            <DataTableCell>
                                                {r.method} {r.path}
                                            </DataTableCell>
                                            <DataTableCell>
                                                {r.params.join(', ')}
                                            </DataTableCell>
                                            <DataTableCell>
                                                {r.client}
                                            </DataTableCell>
                                        </DataTableRow>
                                    )
                                })}
                            </DataTableBody>
                        </DataTable>
                    </>
                )}
            </div>
            <div className={styles.card}>
                <h2 className={styles.subtitle}>{i18n.t('Refused changes')}</h2>
                <div className={styles.muted}>
                    {i18n.t(
                        'Values are masked (***) – central staff see which field caused it, not what was entered. The hub removes a change from this list once it is resolved.'
                    )}
                </div>
                {(hb?.issues ?? 0) > issues.length && (
                    <div className={styles.muted}>
                        {i18n.t('The {{- shown}} most recent of {{- total}}', {
                            shown: issues.length,
                            total: hb?.issues,
                        })}
                    </div>
                )}
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
                                {canManage && <DataTableColumnHeader />}
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
                                    {canManage && (
                                        <DataTableCell>
                                            <DiscardChange
                                                issue={{
                                                    key: is.key,
                                                    hub: hub.id,
                                                    kind: is.kind,
                                                    objectUid: is.objectUid,
                                                    codes: issueCodes(is),
                                                }}
                                            />
                                        </DataTableCell>
                                    )}
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
            </div>
            {canManage && <RevokeTokens hub={hub} />}
            {approving && settings?.group && (
                <ApproveDialog
                    hub={hub}
                    settings={settings}
                    onClose={() => setApproving(false)}
                />
            )}
        </div>
    )
}
