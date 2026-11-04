import i18n from '@dhis2/d2-i18n'
import {
    Button,
    ButtonStrip,
    Modal,
    ModalActions,
    ModalContent,
    ModalTitle,
} from '@dhis2/ui'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import React, { useState } from 'react'
import { useApi } from '@/api/useApi'
import { useMe } from '@/hooks/useMe'
import { when } from '@/hubs/format'
import {
    discardChange,
    discardOf,
    IssueRef,
    readRetries,
    retryId,
} from '@/hubs/refusals'
import { AccountsSettings, useAdminEntry } from '@/hubs/useHubs'

/**
 * Discard for one refused change on a hub's page (hub managers only): the hub throws
 * the change away, with what it blocked, at its next sync.
 */
export const DiscardChange = ({ issue }: { issue: IssueRef }) => {
    const api = useApi()
    const client = useQueryClient()
    const { me } = useMe()
    const { value: settings } = useAdminEntry<AccountsSettings>(
        'facility-hub-admin',
        'accounts'
    )
    const retries = useQuery({
        queryKey: ['retries'],
        queryFn: () => readRetries(api),
    })
    const [confirming, setConfirming] = useState(false)
    const discard = useMutation(
        () =>
            discardChange(api, issue, {
                by: me?.username ?? '',
                at: new Date().toISOString(),
                id: retryId(),
                managers: settings?.managers,
            }),
        {
            onSuccess: () => {
                setConfirming(false)
                client.invalidateQueries(['retries'])
            },
        }
    )
    const asked = discardOf(retries.data ?? [], issue.key)
    if (asked) {
        return (
            <span>
                {i18n.t('Discard asked for {{- when}} by {{- by}}', {
                    when: when(asked.at),
                    by: asked.by,
                })}
            </span>
        )
    }
    return (
        <>
            <Button
                small
                destructive
                disabled={!me || retries.isLoading}
                onClick={() => setConfirming(true)}
            >
                {i18n.t('Discard')}
            </Button>
            {confirming && (
                <Modal small onClose={() => setConfirming(false)}>
                    <ModalTitle>{i18n.t('Discard this change')}</ModalTitle>
                    <ModalContent>
                        {i18n.t(
                            "The hub throws away this refused change to {{- object}}, and what it blocked, at its next sync. The values entered are not sent to central. The hub keeps central's version of the record, or drops it if central never had it. This cannot be undone from the app.",
                            { object: issue.objectUid }
                        )}
                        {discard.error ? (
                            <div>{(discard.error as Error).message}</div>
                        ) : null}
                    </ModalContent>
                    <ModalActions>
                        <ButtonStrip end>
                            <Button onClick={() => setConfirming(false)}>
                                {i18n.t('Cancel')}
                            </Button>
                            <Button
                                destructive
                                loading={discard.isLoading}
                                onClick={() => discard.mutate()}
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
