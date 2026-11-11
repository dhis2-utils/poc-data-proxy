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
    Modal,
    ModalActions,
    ModalContent,
    ModalTitle,
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
    discardGroup,
    groupRefusals,
    isDiscard,
    noLoginCode,
    notAppliedOn,
    partlyReported,
    reachOf,
    readRetries,
    RefusalRow,
    resend,
    Retry,
    retryId,
    scopeOf,
} from '@/hubs/refusals'
import { AccountsSettings, useAdminEntry, useHubs } from '@/hubs/useHubs'

const sameGroup = (a: Retry, b: RefusalRow) =>
    !a.issue &&
    a.kind === b.kind &&
    a.program === b.program &&
    a.code === b.code

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
                orgUnits: me ? scopeOf(me, hubs ?? []) : [],
            }),
        { onSuccess: () => client.invalidateQueries(['retries']) }
    )
    // The group a manager is about to discard, until they confirm.
    const [confirming, setConfirming] = useState<RefusalRow | null>(null)
    const discard = useMutation(
        (g: RefusalRow) =>
            discardGroup(api, g, {
                by: me?.username ?? '',
                at: new Date().toISOString(),
                id: retryId(),
                managers: settings?.managers,
                orgUnits: me ? scopeOf(me, hubs ?? []) : [],
            }),
        {
            onSuccess: () => {
                setConfirming(null)
                client.invalidateQueries(['retries'])
            },
        }
    )
    const rows = useMemo(() => groupRefusals(hubs ?? []), [hubs])
    const reach = confirming
        ? reachOf(confirming, hubs ?? [], me ? scopeOf(me, hubs ?? []) : [])
        : { saves: 0, hubs: 0, uncounted: 0 }
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
    const lastFor = (g: RefusalRow) =>
        earlier.find((r) => !isDiscard(r) && sameGroup(r, g))?.at

    return (
        <>
            <div className={styles.muted}>
                {i18n.t(
                    "Saves central refused, added up across the hubs still in use, by kind, programme and error code. Once the cause is fixed on central (a setting, an option, someone's access), Resend asks every hub in your org units to send that group's refused saves again, as their original authors, at its next sync. Central's new answer stands; a save it refuses again shows up here as a fresh refusal. Saves whose author could not log in online for 7 days after central stopped accepting their token (error code NO_LOGIN) can be resent as another user of the hub."
                )}{' '}
                {i18n.t(
                    "When central was changed on purpose and the waiting saves do not fit (a new rule, a closed period, a duplicate), Discard asks every hub in your org units to throw that group away, with what it blocked. Each hub then keeps central's version of the record, or drops a record central never had. Hubs keep a discarded save for 30 days."
                )}{' '}
                {i18n.t(
                    "A superseded save is one whose record central changed after the refusal. The hub shows central's version, and the refused save waits until it is resent (on top of central's version) or discarded."
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
            {discard.error ? (
                <NoticeBox error title={i18n.t('Could not ask for a discard')}>
                    {(discard.error as Error).message}
                </NoticeBox>
            ) : null}
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
                                <DataTableCell>
                                    {g.count}
                                    {g.superseded ? (
                                        <div className={styles.muted}>
                                            {i18n.t(
                                                '{{- n}} superseded (central changed the record since)',
                                                { n: g.superseded }
                                            )}
                                        </div>
                                    ) : null}
                                </DataTableCell>
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
                                    </Button>{' '}
                                    <Button
                                        small
                                        destructive
                                        disabled={!me || discard.isLoading}
                                        onClick={() => setConfirming(g)}
                                    >
                                        {i18n.t('Discard')}
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
                                    {i18n.t('Asked for')}
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
                                    <DataTableCell>
                                        {isDiscard(r)
                                            ? r.issue
                                                ? i18n.t(
                                                      'Discard {{- object}} on {{- hub}}',
                                                      {
                                                          object: r.object,
                                                          hub: r.hub,
                                                      }
                                                  )
                                                : i18n.t('Discard')
                                            : i18n.t('Resend')}
                                    </DataTableCell>
                                    <DataTableCell>{r.kind}</DataTableCell>
                                    <DataTableCell>
                                        {programName(r.program)}
                                    </DataTableCell>
                                    <DataTableCell>{r.code}</DataTableCell>
                                    <DataTableCell>
                                        {isDiscard(r)
                                            ? '—'
                                            : (r.user ??
                                              i18n.t('their authors'))}
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
            {confirming && (
                <Modal small onClose={() => setConfirming(null)}>
                    <ModalTitle>{i18n.t('Discard refused saves')}</ModalTitle>
                    <ModalContent>
                        <p>
                            {i18n.t(
                                "Each hub in your org units (for a superuser, every hub) throws away its {{- kind}} saves refused with {{- code}} in {{- program}}, and what they blocked. The values entered are not sent to central. Each hub keeps central's version of the record, or drops a record central never had. This cannot be undone from the app.",
                                {
                                    kind: confirming.kind,
                                    code: confirming.code,
                                    program: programName(confirming.program),
                                }
                            )}
                        </p>
                        <p>
                            {i18n.t(
                                'As reported here, this reaches {{- saves}} saves on {{- hubs}} hubs, with what they blocked. It also reaches saves of this group refused since each hub last reported (about hourly).',
                                { saves: reach.saves, hubs: reach.hubs }
                            )}
                            {reach.uncounted > 0 &&
                                ' ' +
                                    i18n.t(
                                        '{{- n}} more hubs in these org units have not reported all their refusals here (no report yet, or more groups than their report lists). Their saves of this group are thrown away too.',
                                        { n: reach.uncounted }
                                    )}
                        </p>
                    </ModalContent>
                    <ModalActions>
                        <ButtonStrip end>
                            <Button onClick={() => setConfirming(null)}>
                                {i18n.t('Cancel')}
                            </Button>
                            <Button
                                destructive
                                loading={discard.isLoading}
                                onClick={() => discard.mutate(confirming)}
                            >
                                {i18n.t('Discard')}
                            </Button>
                        </ButtonStrip>
                    </ModalActions>
                </Modal>
            )}
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
