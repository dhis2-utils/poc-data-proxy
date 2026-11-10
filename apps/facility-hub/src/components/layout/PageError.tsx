import i18n from '@dhis2/d2-i18n'
import { NoticeBox } from '@dhis2/ui'
import React from 'react'
import { useRouteError } from 'react-router-dom'
import { PageWrapper } from './PageWrapper'

/**
 * Shown in place of a page that failed to render, for example on a dataStore entry
 * that is not shaped as the app expects. The sidebar stays, and moving to another page
 * clears it.
 */
export const PageError = () => {
    const error = useRouteError()
    return (
        <PageWrapper>
            <NoticeBox error title={i18n.t('This page could not be shown')}>
                {i18n.t(
                    'One of the entries it reads may not be as the app expects. Other pages may still work.'
                )}{' '}
                {error instanceof Error ? error.message : String(error)}
            </NoticeBox>
        </PageWrapper>
    )
}
