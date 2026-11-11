import i18n from '@dhis2/d2-i18n'
import {
    Button,
    ButtonStrip,
    InputField,
    Modal,
    ModalActions,
    ModalContent,
    ModalTitle,
    NoticeBox,
} from '@dhis2/ui'
import { useQueryClient } from '@tanstack/react-query'
import React, { useState } from 'react'
import { useApi } from '@/api/useApi'
import { approve } from '@/hubaccount/approve'
import { addHubCodes } from '@/hubs/adminEntry'
import { AccountsSettings, HubRow } from '@/hubs/useHubs'
import styles from '@/pages/Pages.module.css'

/**
 * Approves a hub's own account with the code the facility reads from the hub's page
 * (docs/FACILITY-HUB-APP.md), showing each step as it runs.
 */
export const ApproveDialog = ({
    hub,
    settings,
    onClose,
}: {
    hub: HubRow
    settings: AccountsSettings
    onClose: () => void
}) => {
    const api = useApi()
    const client = useQueryClient()
    const [code, setCode] = useState('')
    const [steps, setSteps] = useState<string[]>([])
    const [running, setRunning] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [done, setDone] = useState(false)

    const run = async () => {
        setRunning(true)
        setError(null)
        setSteps([])
        try {
            await approve(
                api,
                { hubID: hub.id, code },
                { ...settings, onStep: (s) => setSteps((xs) => [...xs, s]) }
            )
            // Its code in the instructions in force, which it takes only signed.
            setSteps((xs) => [...xs, i18n.t('Signing the hub settings for it')])
            await addHubCodes(api, hub.id)
            setDone(true)
            client.invalidateQueries(['hubs'])
        } catch (e) {
            setError((e as Error).message)
        } finally {
            setRunning(false)
        }
    }

    const facility =
        hub.orgUnit?.displayName ??
        hub.registry.facilityName ??
        hub.registry.facility
    return (
        <Modal onClose={running ? undefined : onClose} position="middle">
            <ModalTitle>
                {i18n.t('Approve the hub of {{- facility}}', { facility })}
            </ModalTitle>
            <ModalContent>
                <div className={styles.page}>
                    <p>
                        {i18n.t(
                            'Call the facility on a number you already have (never use a code from someone who calls you), and ask them to read the code shown on the hub\'s page, under "The hub\'s own account".'
                        )}
                    </p>
                    <p className={styles.muted}>
                        {i18n.t('Hub {{- id}}, set up by {{- by}}', {
                            id: hub.id,
                            by: hub.registry.setupBy ?? '?',
                        })}
                    </p>
                    <InputField
                        label={i18n.t("Code from the hub's page")}
                        placeholder="XXXX-XXXX-XXXX-XXXX"
                        value={code}
                        onChange={({ value }) => setCode(value ?? '')}
                        disabled={running || done}
                        name="code"
                    />
                    {steps.length > 0 && (
                        <ol className={styles.steps}>
                            {steps.map((s) => (
                                <li key={s}>{s}</li>
                            ))}
                        </ol>
                    )}
                    {error && (
                        <NoticeBox error title={i18n.t('Not approved')}>
                            {error}
                        </NoticeBox>
                    )}
                    {done && (
                        <NoticeBox valid title={i18n.t('Approved')}>
                            {i18n.t(
                                'The hub takes its account up at its next refresh (within about an hour, once it is online).'
                            )}
                        </NoticeBox>
                    )}
                </div>
            </ModalContent>
            <ModalActions>
                <ButtonStrip end>
                    <Button onClick={onClose} secondary disabled={running}>
                        {done ? i18n.t('Close') : i18n.t('Cancel')}
                    </Button>
                    {!done && (
                        <Button
                            primary
                            onClick={run}
                            loading={running}
                            disabled={!code.trim() || running}
                        >
                            {i18n.t('Approve')}
                        </Button>
                    )}
                </ButtonStrip>
            </ModalActions>
        </Modal>
    )
}
