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
    InputField,
    NoticeBox,
} from '@dhis2/ui'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import React, { useMemo, useState } from 'react'
import styles from './Pages.module.css'
import { useApi } from '@/api/useApi'
import { ManagersOnly } from '@/components/hub/ManagersOnly'
import { useMe } from '@/hooks/useMe'
import { useNamed } from '@/hooks/useNamed'
import { when } from '@/hubs/format'
import {
    groupRefusals,
    noLoginCode,
    notAppliedOn,
    partlyReported,
    readRetries,
    RefusalRow,
    resend,
    Retry,
    retryId,
} from '@/hubs/refusals'
import { AccountsSettings, useAdminEntry, useHubs } from '@/hubs/useHubs'

const sameGroup = (a: Retry, b: RefusalRow) =>
    a.kind === b.kind && a.program === b.program && a.code === b.code

const groupKey = (g: RefusalRow) => JSON.stringify([g.kind, g.program, g.code])

const Refusals = () => {
    const api = useApi()
    const client = useQueryClient()
    const { me } = useMe()
    const { value: settings } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
    const { hubs, isLoading, error } = useHubs()
    const programs = useNamed('programs')
    const retries = useQuery({
        queryKey: ['retries'],
        queryFn: () => readRetries(api),
    })
    // The user to send a group as, by group: only for saves whose author could not log in.
    const [sendAs, setSendAs] = useState<Record<string, string>>({})
    const send = useMutation(
        (g: RefusalRow) =>
            resend(api, g, {
                by: me?.username ?? '',
                at: new Date().toISOString(),
                id: retryId(),
                managers: settings?.managers,
                user: g.code === noLoginCode ? sendAs[groupKey(g)] : undefined,
            }),
        { onSuccess: () => client.invalidateQueries(['retries']) }
    )
    const rows = useMemo(() => groupRefusals(hubs ?? []), [hubs])
    const partly = useMemo(() => partlyReported(hubs ?? []), [hubs])
    const names = new Map(programs.list.map((p) => [p.id, p.displayName]))
    const programName = (id: string) => (id ? (names.get(id) ?? id) : '—')

    if (isLoading || retries.isLoading) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    const err = error ?? (retries.error as Error | null)
    if (err) {
        return (
            <NoticeBox error title={i18n.t('Could not load the refusals')}>
                {err.message}
            </NoticeBox>
        )
    }
    const earlier = [...(retries.data ?? [])].reverse()
    const lastFor = (g: RefusalRow) => earlier.find((r) => sameGroup(r, g))?.at

    return (
        <>
            <div className={styles.muted}>
                {i18n.t(
                    "Saves central refused, added up across the hubs still in use, by kind, programme and error code. Once the cause is fixed on central (a setting, an option, someone's access), Resend asks every hub to send that group's refused saves again, as their original authors, at its next sync. Central's new answer stands; a save it refuses again shows up here as a fresh refusal. Saves whose author could not log in online for 7 days after central stopped accepting their token (error code NO_LOGIN) can be resent as another user of the hub."
                )}
            </div>
            {partly.length > 0 && (
                <NoticeBox warning title={i18n.t('Not every group is counted')}>
                    {partly.length === 1
                        ? i18n.t(
                              '1 hub has more groups of refused saves than its report lists, so some of its refusals are not counted here.'
                          )
                        : i18n.t(
                              '{{- n}} hubs have more groups of refused saves than their reports list, so some of their refusals are not counted here.',
                              { n: partly.length }
                          )}
                </NoticeBox>
            )}
            {send.error ? (
                <NoticeBox error title={i18n.t('Could not ask for a resend')}>
                    {(send.error as Error).message}
                </NoticeBox>
            ) : null}
            {rows.length === 0 ? (
                <NoticeBox title={i18n.t('No refused saves')}>
                    {i18n.t('No hub in your org units reports refused saves.')}
                </NoticeBox>
            ) : (
                <DataTable>
                    <DataTableHead>
                        <DataTableRow>
                            <DataTableColumnHeader>
                                {i18n.t('Kind')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Programme')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Error code')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Refused saves')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Hubs')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader>
                                {i18n.t('Last resent')}
                            </DataTableColumnHeader>
                            <DataTableColumnHeader />
                        </DataTableRow>
                    </DataTableHead>
                    <DataTableBody>
                        {rows.map((g) => (
                            <DataTableRow key={groupKey(g)}>
                                <DataTableCell>{g.kind}</DataTableCell>
                                <DataTableCell>
                                    {programName(g.program)}
                                </DataTableCell>
                                <DataTableCell>{g.code}</DataTableCell>
                                <DataTableCell>{g.count}</DataTableCell>
                                <DataTableCell>{g.hubs.length}</DataTableCell>
                                <DataTableCell>
                                    {lastFor(g) ? when(lastFor(g)) : '—'}
                                </DataTableCell>
                                <DataTableCell>
                                    {g.code === noLoginCode && (
                                        <InputField
                                            dense
                                            label={i18n.t('Send as user')}
                                            placeholder={i18n.t('username')}
                                            helpText={i18n.t(
                                                'A user of the hub who can send (has logged in online there). The records then appear on central as created by this user. Leave empty to send them as their authors.'
                                            )}
                                            value={sendAs[groupKey(g)] ?? ''}
                                            onChange={({ value }) =>
                                                setSendAs({
                                                    ...sendAs,
                                                    [groupKey(g)]: value ?? '',
                                                })
                                            }
                                        />
                                    )}
                                    <Button
                                        small
                                        disabled={!me || send.isLoading}
                                        loading={
                                            send.isLoading &&
                                            send.variables === g
                                        }
                                        onClick={() => send.mutate(g)}
                                    >
                                        {i18n.t('Resend')}
                                    </Button>
                                </DataTableCell>
                            </DataTableRow>
                        ))}
                    </DataTableBody>
                </DataTable>
            )}
            <div className={styles.card}>
                <h2 className={styles.subtitle}>
                    {i18n.t('Resends asked for')}
                </h2>
                {earlier.length === 0 ? (
                    <div className={styles.muted}>{i18n.t('None yet.')}</div>
                ) : (
                    <DataTable>
                        <DataTableHead>
                            <DataTableRow>
                                <DataTableColumnHeader>
                                    {i18n.t('When')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('By')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Kind')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Programme')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Error code')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Sent as')}
                                </DataTableColumnHeader>
                                <DataTableColumnHeader>
                                    {i18n.t('Not applied')}
                                </DataTableColumnHeader>
                            </DataTableRow>
                        </DataTableHead>
                        <DataTableBody>
                            {earlier.map((r) => (
                                <DataTableRow key={r.id}>
                                    <DataTableCell>{when(r.at)}</DataTableCell>
                                    <DataTableCell>{r.by}</DataTableCell>
                                    <DataTableCell>{r.kind}</DataTableCell>
                                    <DataTableCell>
                                        {programName(r.program)}
                                    </DataTableCell>
                                    <DataTableCell>{r.code}</DataTableCell>
                                    <DataTableCell>
                                        {r.user ?? i18n.t('their authors')}
                                    </DataTableCell>
                                    <DataTableCell>
                                        {notAppliedOn(hubs ?? [], r.id).map(
                                            (n) => (
                                                <div key={n.hub}>
                                                    {n.hub} ({n.reason})
                                                </div>
                                            )
                                        )}
                                    </DataTableCell>
                                </DataTableRow>
                            ))}
                        </DataTableBody>
                    </DataTable>
                )}
                <div className={styles.muted}>
                    {i18n.t('The last 50 are kept.')}
                </div>
            </div>
        </>
    )
}

export const RefusalsPage = () => (
    <div className={styles.page}>
        <h1 className={styles.title}>{i18n.t('Refused saves')}</h1>
        <ManagersOnly>
            <Refusals />
        </ManagersOnly>
    </div>
)
