/** @type {import('@dhis2/cli-app-scripts').D2Config} */
const config = {
    type: 'app',
    name: 'facility-hub',
    title: 'Facility Hub',
    description:
        'The facility hubs of this country: their state, approvals and central moves',
    minDHIS2Version: '2.42',

    entryPoints: {
        app: './src/App.tsx',
    },

    viteConfigExtensions: './viteConfigExtensions.mts',
}

module.exports = config
