import { useConfig } from '@dhis2/app-runtime'
import i18n from '@dhis2/d2-i18n'
import {
    Button,
    ButtonStrip,
    CircularLoader,
    DataTable,
    DataTableBody,
    DataTableCell,
    DataTableColumnHeader,
    DataTableHead,
    DataTableRow,
    InputField,
    NoticeBox,
} from '@dhis2/ui'
import React, { useMemo, useState } from 'react'
import styles from './Pages.module.css'
import { SuperuserOnly } from '@/components/hub/SuperuserOnly'
import { fromBase64 } from '@/hubaccount/approve'
import { checkAddress, moveCode, normalizeAddress } from '@/hubaccount/codes'
import { when } from '@/hubs/format'
import { useCentralMove, useHubs, useMoveKeys, place } from '@/hubs/useHubs'

// A planned move: hubs read it from the current central and switch once the old
// address has stopped answering (docs/DEPLOYMENT.md, section 6).
const PlannedMove = () => {
    const { baseUrl } = useConfig()
    const { move, isLoading, error, set, clear } = useCentralMove()
    const [url, setUrl] = useState('')
    const [from, setFrom] = useState('')
    if (isLoading) {
        return <CircularLoader small />
    }
    const problem = url.trim()
        ? (checkAddress(url) ??
          (normalizeAddress(url).startsWith('http:') &&
          new URL(baseUrl, window.location.href).protocol === 'https:'
              ? i18n.t(
                    'Central runs on https - hubs only move to an https address'
                )
              : null))
        : null
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Planned move')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    "Announce central's new address before a move. Hubs switch to it once this address has stopped answering for an hour, and only if the new one is the same DHIS2."
                )}
            </div>
            {error && (
                <NoticeBox
                    error
                    title={i18n.t('Could not read the planned move')}
                >
                    {error.message}
                </NoticeBox>
            )}
            {move?.url ? (
                <div className={styles.row}>
                    <div>
                        {i18n.t('Announced – {{- url}}', { url: move.url })}
                        {move.from &&
                            ` · ${i18n.t('not before {{- when}}', { when: when(move.from) })}`}
                    </div>
                    <Button
                        small
                        destructive
                        onClick={() => clear.mutate()}
                        loading={clear.isLoading}
                    >
                        {i18n.t('Withdraw')}
                    </Button>
                </div>
            ) : (
                <div className={styles.muted}>
                    {i18n.t('No move announced.')}
                </div>
            )}
            <div className={styles.row}>
                <InputField
                    label={i18n.t('New address')}
                    name="planned"
                    placeholder="https://hmis.example.org"
                    value={url}
                    onChange={({ value }) => setUrl(value ?? '')}
                    error={!!problem}
                    validationText={problem ?? undefined}
                    className={styles.search}
                />
                <InputField
                    label={i18n.t('Not before, your time (optional)')}
                    type="datetime-local"
                    value={from}
                    onChange={({ value }) => setFrom(value ?? '')}
                />
                <Button
                    primary
                    disabled={!url.trim() || !!problem}
                    loading={set.isLoading}
                    onClick={() =>
                        set.mutate({
                            url: normalizeAddress(url),
                            ...(from && { from: new Date(from).toISOString() }),
                        })
                    }
                >
                    {i18n.t('Announce')}
                </Button>
            </div>
            {(set.error || clear.error) && (
                <NoticeBox error title={i18n.t('Not saved')}>
                    {((set.error ?? clear.error) as Error).message}
                </NoticeBox>
            )}
        </div>
    )
}

// Quoted; a cell a spreadsheet would read as a formula starts with ' (facility names
// and hub IDs come from the registry, which hub owners write).
const csvCell = (s: string) =>
    `"${(/^[=+\-@]/.test(s) ? `'${s}` : s).replace(/"/g, '""')}"`

// Move codes after an unplanned move: one per hub, for the new address, computed here
// from the hubs' move keys (docs/FACILITY-HUB-APP.md). Nothing is written.
const MoveCodes = () => {
    const { hubs, isLoading, error } = useHubs()
    const [address, setAddress] = useState('')
    const [shown, setShown] = useState('')
    const keys = useMoveKeys(!!shown)
    const problem = address.trim() ? checkAddress(address) : null

    const rows = useMemo(() => {
        if (!shown || !keys.data) {
            return []
        }
        return (hubs ?? [])
            .filter((h) => !h.retired)
            .map((h) => {
                const key = keys.data.get(h.id)
                return {
                    id: h.id,
                    facility:
                        h.orgUnit?.displayName ?? h.registry.facilityName ?? '',
                    district: place(h.orgUnit) ?? '',
                    code: key ? moveCode(fromBase64(key), shown) : '',
                }
            })
            .sort((a, b) => a.facility.localeCompare(b.facility))
    }, [hubs, keys.data, shown])

    const download = () => {
        const lines = [
            ['Facility', 'District', 'Hub', 'Address', 'Code'].join(','),
            ...rows.map((r) =>
                [r.facility, r.district, r.id, normalizeAddress(shown), r.code]
                    .map(csvCell)
                    .join(',')
            ),
        ]
        const a = document.createElement('a')
        a.href = URL.createObjectURL(
            new Blob([lines.join('\n')], { type: 'text/csv' })
        )
        a.download = 'move-codes.csv'
        a.click()
        setTimeout(() => URL.revokeObjectURL(a.href), 10000)
    }

    if (isLoading) {
        return <CircularLoader small />
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Move codes')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    "After an unplanned move, give each facility central's new address and its hub's code. Staff enter both on the hub's page; the hub checks the code itself and moves only to that address."
                )}
            </div>
            {error && (
                <NoticeBox error title={i18n.t('Could not load the hubs')}>
                    {error.message}
                </NoticeBox>
            )}
            <div className={`${styles.row} ${styles.noPrint}`}>
                <InputField
                    label={i18n.t("Central's new address")}
                    placeholder="https://hmis.example.org"
                    value={address}
                    onChange={({ value }) => setAddress(value ?? '')}
                    error={!!problem}
                    validationText={problem ?? undefined}
                    className={styles.search}
                    name="address"
                />
                <Button
                    primary
                    disabled={!address.trim() || !!problem}
                    onClick={() => setShown(address)}
                >
                    {i18n.t('Make codes')}
                </Button>
            </div>
            {keys.isLoading && shown && <CircularLoader small />}
            {keys.error && (
                <NoticeBox error title={i18n.t('Could not read the move keys')}>
                    {(keys.error as Error).message}
                </NoticeBox>
            )}
            {rows.length > 0 && (
                <>
                    <div>
                        {i18n.t('Codes for {{- address}}', {
                            address: normalizeAddress(shown),
                        })}
                    </div>
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
                                    {i18n.t('Code')}
                                </DataTableColumnHeader>
                            </DataTableRow>
                        </DataTableHead>
                        <DataTableBody>
                            {rows.map((r) => (
                                <DataTableRow key={r.id}>
                                    <DataTableCell>
                                        {r.facility}
                                        <div className={styles.muted}>
                                            {r.district}
                                        </div>
                                    </DataTableCell>
                                    <DataTableCell>{r.id}</DataTableCell>
                                    <DataTableCell>
                                        {r.code ? (
                                            <span
                                                className={styles.code}
                                                data-hub={r.id}
                                            >
                                                {r.code}
                                            </span>
                                        ) : (
                                            <span className={styles.muted}>
                                                {i18n.t(
                                                    'no code – approve first'
                                                )}
                                            </span>
                                        )}
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                    <ButtonStrip className={styles.noPrint}>
                        <Button onClick={() => window.print()}>
                            {i18n.t('Print')}
                        </Button>
                        <Button onClick={download}>
                            {i18n.t('Download CSV')}
                        </Button>
                    </ButtonStrip>
                </>
            )}
        </div>
    )
}

export const MovesPage = () => (
    <SuperuserOnly>
        <div className={styles.page}>
            <h1 className={styles.title}>{i18n.t('Central moves')}</h1>
            <PlannedMove />
            <MoveCodes />
        </div>
    </SuperuserOnly>
)
