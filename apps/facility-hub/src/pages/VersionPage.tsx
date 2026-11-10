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
    MultiSelectField,
    Modal,
    ModalActions,
    ModalContent,
    ModalTitle,
    MultiSelectOption,
    NoticeBox,
    SingleSelectField,
    SingleSelectOption,
} from '@dhis2/ui'
import { useQuery } from '@tanstack/react-query'
import React, { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import styles from './Pages.module.css'
import { ManagersOnly } from '@/components/hub/ManagersOnly'
import { progress } from '@/hubs/progress'
import {
    mergePilots,
    nextStage,
    ReleaseChoice,
    sameChoice,
    stageLabel,
} from '@/hubs/stage'
import { useAdminEntry, useHubs } from '@/hubs/useHubs'
import { GitHubRelease, releasedVersions } from '@/hubs/versions'

// The supplier's releases (upgrade.ReleaseBase): offered in a list when GitHub answers.
const releasesURL =
    'https://api.github.com/repos/dhis2-utils/poc-data-proxy/releases'

const useGitHubVersions = () =>
    useQuery({
        queryKey: ['githubReleases'],
        staleTime: 10 * 60e3,
        retry: false,
        queryFn: async () => {
            const res = await fetch(releasesURL)
            if (!res.ok) {
                return [] as string[]
            }
            return releasedVersions((await res.json()) as GitHubRelease[])
        },
    })

const versionPattern = /^\d+\.\d+\.\d+$/

const Version = () => {
    const { hubs, isLoading: hubsLoading } = useHubs()
    const {
        value: choice,
        isLoading,
        error,
        update: save,
    } = useAdminEntry<ReleaseChoice>('facility-hub-admin', 'release')
    const github = useGitHubVersions()
    const [version, setVersion] = useState('')
    const [pilots, setPilots] = useState<string[]>([])
    // Next stage moves many hubs at once: asked again first.
    const [confirming, setConfirming] = useState(false)
    useEffect(() => {
        if (choice) {
            setVersion(choice.version)
            setPilots(choice.pilotHubs)
        }
    }, [choice])

    const active = useMemo(() => (hubs ?? []).filter((h) => !h.retired), [hubs])
    const names = useMemo(
        () =>
            new Map(
                active.map((h) => [
                    h.id,
                    h.orgUnit?.displayName ?? h.registry.facility ?? h.id,
                ])
            ),
        [active]
    )
    const shown = useMemo(() => new Set(names.keys()), [names])
    if (isLoading || hubsLoading) {
        return <CircularLoader small />
    }
    const current = choice?.version ? choice : null
    const p = current ? progress(hubs ?? [], current) : null
    const write = (
        change: (latest: ReleaseChoice | null) => ReleaseChoice,
        onSettled?: () => void
    ) => save.mutate(change, { onSettled })
    // Next stage, unless the choice changed since the page read it (another manager's
    // Stop).
    const advance = () =>
        current &&
        write(
            (latest) => {
                if (!sameChoice(latest, current)) {
                    throw new Error(
                        i18n.t(
                            'The choice was changed meanwhile. Check it and try again.'
                        )
                    )
                }
                return nextStage(current)
            },
            () => setConfirming(false)
        )
    const versionOk = versionPattern.test(version.trim())
    const listed = github.data ?? []

    return (
        <>
            {error && (
                <NoticeBox error title={i18n.t('Could not read the choice')}>
                    {error.message}
                </NoticeBox>
            )}
            {save.error && (
                <NoticeBox error title={i18n.t('Could not save')}>
                    {(save.error as Error).message}
                </NoticeBox>
            )}
            <div className={styles.card}>
                <h2 className={styles.subtitle}>{i18n.t('Current choice')}</h2>
                {current ? (
                    <div>
                        {i18n.t('Version {{- v}} – {{- stage}}', {
                            v: current.version,
                            stage: stageLabel(current),
                        })}
                    </div>
                ) : (
                    <div className={styles.muted}>
                        {i18n.t(
                            'No version chosen. Hubs stay on the version they run.'
                        )}
                    </div>
                )}
                {current && p && (
                    <>
                        <div className={styles.row}>
                            {i18n.t(
                                '{{- n}} hubs in this stage, {{- on}} of them on {{- v}}, {{- waiting}} not yet, {{- failed}} failed.',
                                {
                                    n: p.inStage,
                                    on: p.onVersion,
                                    v: current.version,
                                    waiting: p.waiting,
                                    failed: p.failed.length,
                                }
                            )}
                        </div>
                        <ButtonStrip>
                            <Button
                                primary
                                disabled={
                                    current.percent >= 100 || save.isLoading
                                }
                                onClick={() => setConfirming(true)}
                            >
                                {i18n.t('Next stage')}
                            </Button>
                            <Button
                                destructive
                                disabled={
                                    (current.percent === 0 &&
                                        current.pilotHubs.length === 0) ||
                                    save.isLoading
                                }
                                onClick={() =>
                                    write((latest) => ({
                                        ...(latest ?? current),
                                        pilotHubs: [],
                                        percent: 0,
                                    }))
                                }
                            >
                                {i18n.t('Stop')}
                            </Button>
                        </ButtonStrip>
                        {confirming && (
                            <Modal small onClose={() => setConfirming(false)}>
                                <ModalTitle>{i18n.t('Next stage')}</ModalTitle>
                                <ModalContent>
                                    {i18n.t(
                                        '{{- stage}} take version {{- v}} at their next check. Hubs never move back to an older version.',
                                        {
                                            stage: stageLabel(
                                                nextStage(current)
                                            ),
                                            v: current.version,
                                        }
                                    )}
                                </ModalContent>
                                <ModalActions>
                                    <ButtonStrip end>
                                        <Button
                                            onClick={() => setConfirming(false)}
                                        >
                                            {i18n.t('Cancel')}
                                        </Button>
                                        <Button
                                            primary
                                            loading={save.isLoading}
                                            onClick={advance}
                                        >
                                            {i18n.t('Next stage')}
                                        </Button>
                                    </ButtonStrip>
                                </ModalActions>
                            </Modal>
                        )}
                        {p.failed.length > 0 && (
                            <DataTable>
                                <DataTableHead>
                                    <DataTableRow>
                                        <DataTableColumnHeader>
                                            {i18n.t('Hub')}
                                        </DataTableColumnHeader>
                                        <DataTableColumnHeader>
                                            {i18n.t('Why the upgrade failed')}
                                        </DataTableColumnHeader>
                                    </DataTableRow>
                                </DataTableHead>
                                <DataTableBody>
                                    {p.failed.map((f) => (
                                        <DataTableRow key={f.id}>
                                            <DataTableCell>
                                                <Link to={`/hubs/${f.id}`}>
                                                    {names.get(f.id) ?? f.id}
                                                </Link>
                                            </DataTableCell>
                                            <DataTableCell>
                                                {f.reason}
                                            </DataTableCell>
                                        </DataTableRow>
                                    ))}
                                </DataTableBody>
                            </DataTable>
                        )}
                    </>
                )}
            </div>
            <div className={styles.card}>
                <h2 className={styles.subtitle}>
                    {i18n.t('Start a version with pilot hubs')}
                </h2>
                <div className={styles.muted}>
                    {i18n.t(
                        'Pilot hubs take the version first. Check them, then move on to 10 %, 50 % and all hubs. Hubs never move to an older version.'
                    )}
                </div>
                {listed.length > 0 && (
                    <SingleSelectField
                        label={i18n.t('Released versions')}
                        selected={listed.includes(version) ? version : ''}
                        onChange={({ selected }) => setVersion(selected ?? '')}
                    >
                        {listed.map((v) => (
                            <SingleSelectOption key={v} value={v} label={v} />
                        ))}
                    </SingleSelectField>
                )}
                <InputField
                    name="version"
                    label={i18n.t('Version')}
                    placeholder="0.3.0"
                    value={version}
                    onChange={({ value }) => setVersion(value ?? '')}
                    error={!!version.trim() && !versionOk}
                    validationText={
                        version.trim() && !versionOk
                            ? i18n.t('A version looks like 0.3.0')
                            : undefined
                    }
                />
                <MultiSelectField
                    dataTest="pilot-hubs"
                    label={i18n.t('Pilot hubs')}
                    filterable
                    selected={pilots.filter((id) => names.has(id))}
                    // Pilots the user does not see stay chosen.
                    onChange={({ selected }) =>
                        setPilots([
                            ...pilots.filter((id) => !names.has(id)),
                            ...selected,
                        ])
                    }
                >
                    {active.map((h) => (
                        <MultiSelectOption
                            key={h.id}
                            value={h.id}
                            label={`${names.get(h.id)} (${h.id})`}
                        />
                    ))}
                </MultiSelectField>
                <ButtonStrip>
                    <Button
                        primary
                        disabled={
                            !versionOk || pilots.length === 0 || save.isLoading
                        }
                        onClick={() =>
                            write((latest) => ({
                                version: version.trim(),
                                pilotHubs: mergePilots(latest, pilots, shown),
                                percent: 0,
                            }))
                        }
                    >
                        {i18n.t('Start with pilot hubs')}
                    </Button>
                </ButtonStrip>
            </div>
        </>
    )
}

export const VersionPage = () => (
    <div className={styles.page}>
        <h1 className={styles.title}>{i18n.t('Hub version')}</h1>
        <ManagersOnly>
            <Version />
        </ManagersOnly>
    </div>
)
