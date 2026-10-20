import i18n from '@dhis2/d2-i18n'
import React from 'react'
import { Link } from 'react-router-dom'
import { when } from '@/hubs/format'
import { replacedBy } from '@/hubs/registry'
import { HubRow } from '@/hubs/useHubs'
import styles from '@/pages/Pages.module.css'

// Named by ID: the hubs of one facility share its name.
const HubLink = ({ id }: { id: string }) => <Link to={`/hubs/${id}`}>{id}</Link>

/**
 * What happened to a hub: replaced, replacing others, a replacement undone, retired by
 * a reset or an undo, tokens revoked. Shown to everyone; nothing when none applies.
 */
export const HubHistory = ({ hub }: { hub: HubRow }) => {
    const r = hub.registry
    const by = replacedBy(r)
    const lines: React.ReactNode[] = []
    if (r.status === 'revoked') {
        lines.push(
            i18n.t('Tokens revoked by {{- by}} on {{- when}}.', {
                by: r.revokedBy ?? '?',
                when: when(r.revokedAt),
            })
        )
    }
    if (by) {
        lines.push(
            <>
                {i18n.t('Replaced by')} <HubLink id={by} />{' '}
                {i18n.t('({{- by}}, {{- when}}).', {
                    by: r.retiredByUser ?? '?',
                    when: when(r.retiredAt),
                })}
            </>
        )
    }
    if (r.status === 'retired' && r.retiredBy === 'reset') {
        lines.push(
            i18n.t('Retired when it was reset ({{- by}}, {{- when}}).', {
                by: r.retiredByUser ?? '?',
                when: when(r.retiredAt),
            })
        )
    }
    if (r.status === 'retired' && r.retiredBy === 'undo') {
        lines.push(
            i18n.t(
                'Retired because its replacement of an earlier hub was undone ({{- by}}, {{- when}}).',
                { by: r.retiredByUser ?? '?', when: when(r.retiredAt) }
            )
        )
    }
    if (r.replaces?.length) {
        lines.push(
            <>
                {i18n.t('Replaces')}{' '}
                {r.replaces.map((id, i) => (
                    <React.Fragment key={id}>
                        {i > 0 && ', '}
                        <HubLink id={id} />
                    </React.Fragment>
                ))}
            </>
        )
    }
    if (r.undone) {
        lines.push(
            <>
                {i18n.t('Its replacement by')}{' '}
                <HubLink id={r.undone.retiredBy} />{' '}
                {i18n.t('was undone ({{- by}}, {{- when}}).', {
                    by: r.undone.by,
                    when: when(r.undone.at),
                })}
            </>
        )
    }
    if (lines.length === 0) {
        return null
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>{i18n.t('Replacements')}</h2>
            {lines.map((l, i) => (
                <div key={i}>{l}</div>
            ))}
        </div>
    )
}
