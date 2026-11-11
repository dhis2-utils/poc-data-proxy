import i18n from '@dhis2/d2-i18n'
import {
    Button,
    ButtonStrip,
    CircularLoader,
    InputField,
    MultiSelectField,
    MultiSelectOption,
    NoticeBox,
    SingleSelectField,
    SingleSelectOption,
} from '@dhis2/ui'
import { useQuery } from '@tanstack/react-query'
import React, { useEffect, useState } from 'react'
import styles from './Pages.module.css'
import { useApi } from '@/api/useApi'
import { ManagersOnly } from '@/components/hub/ManagersOnly'
import { useCanManage } from '@/hooks/useCanManage'
import { Named, useNamed } from '@/hooks/useNamed'
import { shareAccountsGroup, shareAdminEntries } from '@/hubs/adminEntry'
import {
    aggregateForm,
    aggregateValue,
    AggregateRetention,
    Limits,
    limits,
    LocalReads,
    localReadsValue,
    MetadataReload,
    metadataReloadValue,
    Retention,
    retentionForm,
    retentionValue,
    Row,
    poolForm,
    poolValue,
    ReservedPool,
    TokenSettings,
    tokensForm,
    tokensValue,
    wholeNumber,
} from '@/hubs/settings'
import { AccountsSettings, accountsValue, useAdminEntry } from '@/hubs/useHubs'

// The hub accounts group and role, and the hub managers and hub owners groups: set up
// once for the country, by a superuser, and shown to superusers only. The managers and
// owners groups cannot be changed once named (accountsValue).
export const AccountsSettingsCard = () => {
    const api = useApi()
    const {
        value: settings,
        isLoading,
        update: save,
    } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts',
        async (saved, latest) => {
            // Entries made before the managers were named.
            if (saved.managers && !latest?.managers) {
                await shareAdminEntries(api, saved.managers)
            }
            if (saved.group) {
                await shareAccountsGroup(api, {
                    group: saved.group,
                    managers: saved.managers,
                    owners: saved.owners,
                })
            }
        }
    )
    const { superuser } = useCanManage()
    const choices = useQuery({
        queryKey: ['groupsAndRoles'],
        enabled: superuser,
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

    const locked = i18n.t(
        'Change who is in this group in the Users app, not the group itself'
    )

    if (!superuser) {
        return null
    }
    if (isLoading || choices.isLoading) {
        return <CircularLoader small />
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Hub accounts')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Each approved hub gets a user in this group (write sharing on the facility-hub namespaces, except facility-hub-admin) with this role, which must have no authorities. Members of the hub managers group approve hubs and make move codes without being superusers; they need a role with F_USER_ADD, F_USER_VIEW, F_USER_GROUPS_READ_ONLY_ADD_MEMBERS and M_facilityhub. Saving gives the hub managers and hub owners groups read access to the hub accounts group, because central drops, without an error, a group the person sharing cannot see. Each hub makes its registry entry writable by the hub owners group (staff who set hubs up, take them over or replace them), the hub accounts group and the hub managers only, once the hub owners group is named. Only superusers change these settings, and the hub managers and hub owners groups only until they are named.'
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
                <SingleSelectField
                    label={i18n.t('Hub managers group')}
                    selected={managers}
                    onChange={({ selected }) => setManagers(selected)}
                    disabled={!!settings?.managers}
                    helpText={settings?.managers ? locked : undefined}
                    filterable
                    clearable={!settings?.managers}
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
                    dataTest="owners-group"
                    selected={owners}
                    onChange={({ selected }) => setOwners(selected)}
                    disabled={!!settings?.owners}
                    helpText={settings?.owners ? locked : undefined}
                    filterable
                    clearable={!settings?.owners}
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
                    onClick={() => {
                        // Written on the entry as central has it now: a group another
                        // superuser named meanwhile stays.
                        save.mutate((latest) =>
                            accountsValue(
                                { group, role, managers, owners },
                                latest
                            )
                        )
                    }}
                    loading={save.isLoading}
                    // Open when nothing changed too: saving again shares the
                    // entries and the hub accounts group again, as a hub's
                    // "dropped groups" warning asks.
                    disabled={!group || !role}
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

const rangeText = (l: Limits) =>
    i18n.t('A whole number from {{- min}} to {{- max}}', l)

const NumberField = ({
    label,
    value,
    onChange,
    l,
    help,
}: {
    label: string
    value: string
    onChange: (v: string) => void
    l: Limits
    help?: string
}) => {
    const bad = wholeNumber(value, l) === null
    return (
        <InputField
            label={label}
            value={value}
            inputWidth="120px"
            onChange={({ value }) => onChange(value ?? '')}
            error={bad}
            validationText={bad ? rangeText(l) : undefined}
            helpText={help}
        />
    )
}

/** A number for each chosen programme, person type or data set. */
const RowsEditor = ({
    title,
    addLabel,
    options,
    rows,
    onChange,
    l,
    initial,
}: {
    title: string
    addLabel: string
    options: { list: Named[]; isLoading: boolean; error: Error | null }
    rows: Row[]
    onChange: (rows: Row[]) => void
    l: Limits
    initial: string
}) => {
    const names = new Map(options.list.map((o) => [o.id, o.displayName]))
    const chosen = new Set(rows.map((r) => r.id))
    return (
        <div className={styles.page}>
            <h3 className={styles.subtitle}>{title}</h3>
            {options.error && (
                <NoticeBox error title={i18n.t('Could not load the list')}>
                    {options.error.message}
                </NoticeBox>
            )}
            {rows.map((r) => (
                <div key={r.id} className={styles.row}>
                    <NumberField
                        label={names.get(r.id) ?? r.id}
                        value={r.value}
                        l={l}
                        onChange={(value) =>
                            onChange(
                                rows.map((x) =>
                                    x.id === r.id ? { ...x, value } : x
                                )
                            )
                        }
                    />
                    <Button
                        small
                        secondary
                        onClick={() =>
                            onChange(rows.filter((x) => x.id !== r.id))
                        }
                    >
                        {i18n.t('Remove')}
                    </Button>
                </div>
            ))}
            {options.isLoading ? (
                <CircularLoader small />
            ) : (
                <SingleSelectField
                    className={styles.picker}
                    placeholder={addLabel}
                    filterable
                    noMatchText={i18n.t('No match')}
                    selected=""
                    onChange={({ selected }) =>
                        selected &&
                        onChange([...rows, { id: selected, value: initial }])
                    }
                >
                    {options.list
                        .filter((o) => !chosen.has(o.id))
                        .map((o) => (
                            <SingleSelectOption
                                key={o.id}
                                value={o.id}
                                label={o.displayName}
                            />
                        ))}
                </SingleSelectField>
            )}
        </div>
    )
}

/** Save button and outcome for one settings key. */
const SaveBar = ({
    valid,
    unset,
    save,
}: {
    valid: boolean
    unset: boolean
    save: {
        isLoading: boolean
        isSuccess: boolean
        error: unknown
        mutate: () => void
    }
}) => (
    <>
        {unset && !save.isSuccess && (
            <div className={styles.muted}>
                {i18n.t(
                    'Not set yet, so the values shown are the defaults the hubs use.'
                )}
            </div>
        )}
        {save.error ? (
            <NoticeBox error title={i18n.t('Could not save')}>
                {(save.error as Error).message}
            </NoticeBox>
        ) : null}
        {save.isSuccess && (
            <NoticeBox valid title={i18n.t('Saved')}>
                {i18n.t('Hubs take it up at their next hourly refresh.')}
            </NoticeBox>
        )}
        <ButtonStrip>
            <Button
                primary
                disabled={!valid || save.isLoading}
                loading={save.isLoading}
                onClick={() => save.mutate()}
            >
                {i18n.t('Save')}
            </Button>
        </ButtonStrip>
    </>
)

const TrackerRetention = ({ stored }: { stored: Retention | null }) => {
    const { save } = useAdminEntry<Retention>(
        'facility-hub-config',
        'retention'
    )
    const programs = useNamed('programs')
    const types = useNamed('trackedEntityTypes')
    const [form, setForm] = useState(() => retentionForm(stored))
    const value = retentionValue(form, stored)
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Tracker retention')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Months of data each hub keeps for offline use. 0 keeps everything. Event programmes keep events by event date; tracker programmes keep active enrollments in full and closed ones until their last change is older. Persons are kept unless their type has a number of months here.'
                )}
            </div>
            <NumberField
                label={i18n.t('Months kept by default')}
                value={form.defaultMonths}
                l={limits.months}
                onChange={(defaultMonths) =>
                    setForm({ ...form, defaultMonths })
                }
            />
            <RowsEditor
                title={i18n.t('By programme')}
                addLabel={i18n.t('Add a programme')}
                options={programs}
                rows={form.programs}
                onChange={(rows) => setForm({ ...form, programs: rows })}
                l={limits.months}
                initial={form.defaultMonths}
            />
            <RowsEditor
                title={i18n.t('By person type')}
                addLabel={i18n.t('Add a person type')}
                options={types}
                rows={form.trackedEntityTypes}
                onChange={(rows) =>
                    setForm({ ...form, trackedEntityTypes: rows })
                }
                l={limits.months}
                initial="0"
            />
            <SaveBar
                valid={!!value}
                unset={!stored}
                save={{ ...save, mutate: () => value && save.mutate(value) }}
            />
        </div>
    )
}

const LocalReadsCard = ({ stored }: { stored: LocalReads | null }) => {
    const { save } = useAdminEntry<LocalReads>(
        'facility-hub-config',
        'localReads'
    )
    const programs = useNamed('programs')
    const [chosen, setChosen] = useState<string[]>(() => stored?.programs ?? [])
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>
                {i18n.t('Answer from the hub while online')}
            </h2>
            <div className={styles.muted}>
                {i18n.t(
                    "For these programmes, each hub answers staff from its own copy even when central is reachable, so screens do not wait on a slow or unreliable link. Saves still go to central at once. Changes made elsewhere (another facility, a district officer) reach staff at the hub's next sync, 15 minutes by default. Searches across the district still go to central, unless a user's search scope is their facility alone. Hubs read this setting within an hour."
                )}
            </div>
            {programs.error && (
                <NoticeBox error title={i18n.t('Could not load the list')}>
                    {programs.error.message}
                </NoticeBox>
            )}
            {programs.isLoading ? (
                <CircularLoader small />
            ) : (
                <MultiSelectField
                    className={styles.picker}
                    label={i18n.t('Programmes')}
                    placeholder={i18n.t(
                        'None, so everything goes to central while online'
                    )}
                    filterable
                    noMatchText={i18n.t('No match')}
                    selected={chosen.filter((id) =>
                        programs.list.some((o) => o.id === id)
                    )}
                    onChange={({ selected }) => setChosen(selected)}
                >
                    {programs.list.map((o) => (
                        <MultiSelectOption
                            key={o.id}
                            value={o.id}
                            label={o.displayName}
                        />
                    ))}
                </MultiSelectField>
            )}
            <SaveBar
                valid
                unset={!stored}
                save={{
                    ...save,
                    mutate: () => save.mutate(localReadsValue(chosen, stored)),
                }}
            />
        </div>
    )
}

const MetadataReloadCard = ({ stored }: { stored: MetadataReload | null }) => {
    const { save } = useAdminEntry<MetadataReload>(
        'facility-hub-config',
        'metadataReload'
    )
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>
                {i18n.t('Refresh metadata on all hubs')}
            </h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Hubs that answer from their own copy while online notice most metadata changes within an hour. They do not notice deletions, changes to which org units a programme or data set is assigned to, or changes to settings. After such a change, or to roll out a fix at once, refresh here. Every online hub then fetches metadata and settings from central again within 15 minutes, and an offline hub when it reconnects.'
                )}
            </div>
            <div className={styles.muted}>
                {stored?.at
                    ? i18n.t('Last refresh asked for {{- at}}', {
                          at: new Date(stored.at).toLocaleString(),
                      })
                    : i18n.t('Never asked for.')}
            </div>
            {save.error ? (
                <NoticeBox error title={i18n.t('Could not save')}>
                    {(save.error as Error).message}
                </NoticeBox>
            ) : null}
            <ButtonStrip>
                <Button
                    loading={save.isLoading}
                    onClick={() =>
                        save.mutate(metadataReloadValue(new Date(), stored))
                    }
                >
                    {i18n.t('Refresh metadata on all hubs')}
                </Button>
            </ButtonStrip>
        </div>
    )
}

const AggregateRetentionCard = ({
    stored,
}: {
    stored: AggregateRetention | null
}) => {
    const { save } = useAdminEntry<AggregateRetention>(
        'facility-hub-config',
        'aggregate'
    )
    const dataSets = useNamed('dataSets')
    const [form, setForm] = useState(() => aggregateForm(stored))
    const value = aggregateValue(form, stored)
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Aggregate retention')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    'Periods of each data set a hub keeps for offline use, the current one included.'
                )}
            </div>
            <NumberField
                label={i18n.t('Periods kept by default')}
                value={form.retentionPeriods}
                l={limits.periods}
                onChange={(retentionPeriods) =>
                    setForm({ ...form, retentionPeriods })
                }
            />
            <RowsEditor
                title={i18n.t('By data set')}
                addLabel={i18n.t('Add a data set')}
                options={dataSets}
                rows={form.dataSets}
                onChange={(rows) => setForm({ ...form, dataSets: rows })}
                l={limits.periods}
                initial={form.retentionPeriods}
            />
            <SaveBar
                valid={!!value}
                unset={!stored}
                save={{ ...save, mutate: () => value && save.mutate(value) }}
            />
        </div>
    )
}

const TokenLifetime = ({ stored }: { stored: TokenSettings | null }) => {
    const { save } = useAdminEntry<TokenSettings>(
        'facility-hub-config',
        'tokens'
    )
    const [days, setDays] = useState(() => tokensForm(stored))
    const value = tokensValue(days, stored)
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Token lifetime')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    "Days that the tokens hubs make for their users and themselves stay valid. New and renewed tokens take it; a token with more life left is renewed early. Without a setting here, each hub uses its own configuration's value."
                )}
            </div>
            <NumberField
                label={i18n.t('Days')}
                value={days}
                l={limits.expiryDays}
                onChange={setDays}
            />
            <SaveBar
                valid={!!value}
                unset={!stored}
                save={{ ...save, mutate: () => value && save.mutate(value) }}
            />
        </div>
    )
}

const ReservedValues = ({ stored }: { stored: ReservedPool | null }) => {
    const { save } = useAdminEntry<ReservedPool>(
        'facility-hub-config',
        'reserved'
    )
    const attributes = useNamed('trackedEntityAttributes')
    const [form, setForm] = useState(() => poolForm(stored))
    const value = poolValue(form, stored)
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Reserved values')}</h2>
            <div className={styles.muted}>
                {i18n.t(
                    "How many generated values (patient numbers, for example) each hub keeps in reserve for offline registration. The hub refills the pool from central when it falls below a quarter of this, in requests of at most 1000 values. Without a setting here, each hub uses its own configuration's default (500). Set an attribute's pool to 0 to turn its offline pool off."
                )}
            </div>
            <NumberField
                label={i18n.t('Values kept by default')}
                value={form.defaultPool}
                l={limits.pool}
                onChange={(defaultPool) => setForm({ ...form, defaultPool })}
            />
            <RowsEditor
                title={i18n.t('By generated attribute')}
                addLabel={i18n.t('Add an attribute')}
                options={attributes}
                rows={form.attributes}
                onChange={(rows) => setForm({ ...form, attributes: rows })}
                l={limits.poolRow}
                initial={form.defaultPool}
            />
            <SaveBar
                valid={!!value}
                unset={!stored}
                save={{ ...save, mutate: () => value && save.mutate(value) }}
            />
        </div>
    )
}

const Settings = () => {
    const retention = useAdminEntry<Retention>(
        'facility-hub-config',
        'retention'
    )
    const aggregate = useAdminEntry<AggregateRetention>(
        'facility-hub-config',
        'aggregate'
    )
    const tokens = useAdminEntry<TokenSettings>('facility-hub-config', 'tokens')
    const reserved = useAdminEntry<ReservedPool>(
        'facility-hub-config',
        'reserved'
    )
    const localReads = useAdminEntry<LocalReads>(
        'facility-hub-config',
        'localReads'
    )
    const metadataReload = useAdminEntry<MetadataReload>(
        'facility-hub-config',
        'metadataReload'
    )
    if (
        localReads.isLoading ||
        metadataReload.isLoading ||
        retention.isLoading ||
        aggregate.isLoading ||
        tokens.isLoading ||
        reserved.isLoading
    ) {
        return (
            <div className={styles.loading}>
                <CircularLoader />
            </div>
        )
    }
    const error =
        retention.error ??
        aggregate.error ??
        tokens.error ??
        reserved.error ??
        localReads.error ??
        metadataReload.error
    if (error) {
        return (
            <NoticeBox error title={i18n.t('Could not read the settings')}>
                {error.message}
            </NoticeBox>
        )
    }
    return (
        <>
            <AccountsSettingsCard />
            <div className={styles.muted}>
                {i18n.t(
                    'These settings apply to every hub. Anyone can read them; only hub managers and superusers change them.'
                )}
            </div>
            <TrackerRetention stored={retention.value ?? null} />
            <AggregateRetentionCard stored={aggregate.value ?? null} />
            <LocalReadsCard stored={localReads.value ?? null} />
            <MetadataReloadCard stored={metadataReload.value ?? null} />
            <TokenLifetime stored={tokens.value ?? null} />
            <ReservedValues stored={reserved.value ?? null} />
        </>
    )
}

export const SettingsPage = () => (
    <div className={styles.page}>
        <h1 className={styles.title}>{i18n.t('Settings')}</h1>
        <ManagersOnly>
            <Settings />
        </ManagersOnly>
    </div>
)
