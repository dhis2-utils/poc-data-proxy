import i18n from '@dhis2/d2-i18n'
import { Button, ButtonStrip, NoticeBox } from '@dhis2/ui'
import { useQueryClient } from '@tanstack/react-query'
import React, { useState } from 'react'
import { useApi } from '@/api/useApi'
import { useMe } from '@/hooks/useMe'
import {
    replacedBy,
    RevokeResult,
    revokeTokens,
    undoReplacement,
} from '@/hubs/registry'
import { HubRow } from '@/hubs/useHubs'
import styles from '@/pages/Pages.module.css'

/** A step a manager confirms on the page before it runs, and its outcome. */
const useStep = <T,>(run: () => Promise<T>) => {
    const client = useQueryClient()
    const [confirming, setConfirming] = useState(false)
    const [running, setRunning] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [result, setResult] = useState<T | null>(null)
    const go = async () => {
        setRunning(true)
        setError(null)
        try {
            setResult(await run())
            setConfirming(false)
            client.invalidateQueries(['hubs'])
        } catch (e) {
            setError((e as Error).message)
        } finally {
            setRunning(false)
        }
    }
    return { confirming, setConfirming, running, error, result, go }
}

const revokedText = (r: RevokeResult) => {
    const deleted =
        r.listed === 0
            ? i18n.t('The hub listed no tokens, so none was deleted.')
            : r.deleted === 1
              ? i18n.t('1 of {{- n}} listed tokens deleted.', { n: r.listed })
              : i18n.t('{{- d}} of {{- n}} listed tokens deleted.', {
                    d: r.deleted,
                    n: r.listed,
                })
    const failed =
        r.failed.length === 0
            ? ''
            : r.failed.length === 1
              ? i18n.t(
                    '1 token could not be deleted (already gone, or not shared with the hub managers).'
                )
              : i18n.t(
                    '{{- n}} tokens could not be deleted (already gone, or not shared with the hub managers).',
                    { n: r.failed.length }
                )
    const account = {
        disabled: i18n.t("The hub's own DHIS2 user is disabled."),
        password: i18n.t(
            "The hub's own DHIS2 user could not be disabled; it has a new random password."
        ),
        none: '',
        failed: i18n.t(
            "The hub's own DHIS2 user could not be disabled. Disable it in the Users app."
        ),
    }[r.account]
    return [deleted, failed, account].filter(Boolean).join(' ')
}

/** "Revoke all tokens of this hub" (spec 2026-10-20, section 2), for managers. */
export const RevokeTokens = ({ hub }: { hub: HubRow }) => {
    const api = useApi()
    const { me } = useMe()
    const step = useStep(() =>
        revokeTokens(api, hub.id, {
            by: me?.username ?? '',
            at: new Date().toISOString(),
        })
    )
    if (hub.registry.status === 'revoked' && !step.result) {
        return null
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>
                {i18n.t('A lost or stolen hub')}
            </h2>
            {!step.result && (
                <div className={styles.muted}>
                    {i18n.t(
                        'Deletes every token the hub lists and marks it revoked. A revoked hub that connects again stops serving data, deletes the tokens it still holds and sends nothing more.'
                    )}
                </div>
            )}
            {!step.result && !step.confirming && (
                <ButtonStrip>
                    <Button
                        destructive
                        onClick={() => step.setConfirming(true)}
                        disabled={!me}
                    >
                        {i18n.t('Revoke all tokens of this hub')}
                    </Button>
                </ButtonStrip>
            )}
            {step.confirming && (
                <NoticeBox
                    warning
                    title={i18n.t('Revoke all tokens of this hub?')}
                >
                    <p>
                        {i18n.t(
                            'The hub stops working for its facility and cannot be used again. Unsent saves stay on its disk.'
                        )}
                    </p>
                    <ButtonStrip>
                        <Button
                            destructive
                            onClick={step.go}
                            loading={step.running}
                            disabled={step.running}
                        >
                            {i18n.t('Revoke')}
                        </Button>
                        <Button
                            secondary
                            onClick={() => step.setConfirming(false)}
                            disabled={step.running}
                        >
                            {i18n.t('Cancel')}
                        </Button>
                    </ButtonStrip>
                </NoticeBox>
            )}
            {step.error && (
                <NoticeBox error title={i18n.t('Not revoked')}>
                    {step.error}
                </NoticeBox>
            )}
            {step.result && (
                <NoticeBox valid title={i18n.t('Hub revoked')}>
                    <p>{revokedText(step.result)}</p>
                    <p>
                        {i18n.t(
                            "Have the hub's users change their passwords now. Tokens the hub did not list (made before this release and not yet renewed) or that could not be deleted cannot be revoked from here, and keep working against central until they expire."
                        )}
                    </p>
                </NoticeBox>
            )}
        </div>
    )
}

/** "Undo this replacement" (spec 2026-10-20, section 3), for managers. */
export const UndoReplacement = ({ hub }: { hub: HubRow }) => {
    const api = useApi()
    const { me } = useMe()
    const step = useStep(() =>
        undoReplacement(api, hub.id, {
            by: me?.username ?? '',
            at: new Date().toISOString(),
        }).then(() => true)
    )
    const by = replacedBy(hub.registry)
    if (!by && !step.result) {
        return null
    }
    return (
        <div className={styles.card}>
            <h2 className={styles.subtitle}>
                {i18n.t('A mistaken replacement')}
            </h2>
            {!step.result && !step.confirming && (
                <ButtonStrip>
                    <Button
                        onClick={() => step.setConfirming(true)}
                        disabled={!me}
                    >
                        {i18n.t('Undo this replacement')}
                    </Button>
                </ButtonStrip>
            )}
            {step.confirming && (
                <NoticeBox warning title={i18n.t('Undo this replacement?')}>
                    <p>
                        {i18n.t(
                            'This hub becomes active again and {{- id}}, the hub that replaced it, is retired. Both pick it up within one sync cycle.',
                            { id: by }
                        )}
                    </p>
                    <ButtonStrip>
                        <Button
                            primary
                            onClick={step.go}
                            loading={step.running}
                            disabled={step.running}
                        >
                            {i18n.t('Undo')}
                        </Button>
                        <Button
                            secondary
                            onClick={() => step.setConfirming(false)}
                            disabled={step.running}
                        >
                            {i18n.t('Cancel')}
                        </Button>
                    </ButtonStrip>
                </NoticeBox>
            )}
            {step.error && (
                <NoticeBox error title={i18n.t('Not undone')}>
                    {step.error}
                </NoticeBox>
            )}
            {step.result && (
                <NoticeBox valid title={i18n.t('Replacement undone')}>
                    {i18n.t(
                        'This hub is active again; the hub that replaced it is retired.'
                    )}
                </NoticeBox>
            )}
        </div>
    )
}
