targetScope = 'resourceGroup'

@description('Location for all resources.')
param location string = resourceGroup().location

@description('Globally unique name of the Function App.')
@minLength(2)
@maxLength(60)
param functionAppName string

@description('Globally unique name of the storage account. Use 3-24 lowercase alphanumeric characters.')
@minLength(3)
@maxLength(24)
param storageAccountName string

@description('Globally unique name of the Key Vault.')
@minLength(3)
@maxLength(24)
param keyVaultName string

@description('Name of the Log Analytics workspace.')
@minLength(4)
@maxLength(63)
param logAnalyticsWorkspaceName string = '${take(functionAppName, 43)}-law'

@description('Name of the Application Insights component.')
@minLength(4)
@maxLength(260)
param applicationInsightsName string = '${functionAppName}-appi'

@description('Name of the Flex Consumption plan.')
@minLength(2)
@maxLength(40)
param appServicePlanName string = '${take(functionAppName, 35)}-plan'

@description('Name of the user-assigned managed identity attached to the Function App.')
@minLength(2)
@maxLength(128)
param userAssignedIdentityName string = '${functionAppName}-uai'

@description('Optional custom domain for the Function App, typically fronted by Cloudflare.')
param customDomainName string = ''

@description('Set to true only after the required Cloudflare TXT and CNAME records are in place.')
param createCustomDomainBinding bool = false

@description('Tesla OAuth client ID.')
@minLength(1)
param teslaClientId string

@description('Tesla OAuth client secret value to seed into Key Vault.')
@secure()
param teslaClientSecret string

@description('Redirect URI registered with Tesla.')
@minLength(1)
param teslaRedirectUri string

@description('Tesla Fleet region.')
@allowed([
  'na'
  'eu'
  'cn'
])
param teslaRegion string = 'na'

@description('Tesla OAuth scope string.')
param teslaScope string = 'openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds'

@description('Tesla public key PEM to seed into Key Vault.')
@secure()
param teslaPublicKeyPem string

@description('HMAC key used to sign OAuth state values.')
@secure()
param oauthStateHmacKey string

@description('Key Vault secret name used by the function at runtime to store the refresh token.')
param teslaRefreshTokenSecretName string = 'tesla-refresh-token'

@description('Key Vault secret name for the Tesla client secret.')
param teslaClientSecretSecretName string = 'tesla-client-secret'

@description('Key Vault secret name for the Tesla public key PEM.')
param teslaPublicKeySecretName string = 'tesla-public-key-pem'

@description('Key Vault secret name for the OAuth state HMAC key.')
param oauthStateHmacKeySecretName string = 'oauth-state-hmac-key'

@description('Maximum scale-out instance count for the Flex Consumption app.')
@minValue(40)
@maxValue(1000)
param maximumInstanceCount int = 100

@description('Instance memory size in MB for the Flex Consumption app.')
@allowed([
  2048
  4096
])
param instanceMemoryMB int = 2048

@description('Enable purge protection on the Key Vault.')
param enableKeyVaultPurgeProtection bool = true

@description('Retention period for soft-deleted Key Vault items.')
@minValue(7)
@maxValue(90)
param keyVaultSoftDeleteRetentionInDays int = 90

@description('Resource tags applied to all supported resources.')
param tags object = {}

var deploymentStorageContainerName = 'function-releases'
var blobServiceUri = 'https://${storageAccountName}.blob.${environment().suffixes.storage}'
var queueServiceUri = 'https://${storageAccountName}.queue.${environment().suffixes.storage}'
var tableServiceUri = 'https://${storageAccountName}.table.${environment().suffixes.storage}'
var deploymentStorageContainerUri = '${blobServiceUri}/${deploymentStorageContainerName}'

module userAssignedIdentity 'br/public:avm/res/managed-identity/user-assigned-identity:0.5.0' = {
  name: 'userAssignedIdentity'
  params: {
    location: location
    name: userAssignedIdentityName
    tags: tags
  }
}

module logAnalytics 'br/public:avm/res/operational-insights/workspace:0.15.0' = {
  name: 'logAnalytics'
  params: {
    features: {
      disableLocalAuth: true
      enableLogAccessUsingOnlyResourcePermissions: true
    }
    location: location
    name: logAnalyticsWorkspaceName
    skuName: 'PerGB2018'
    tags: tags
  }
}

module applicationInsights 'br/public:avm/res/insights/component:0.7.1' = {
  name: 'applicationInsights'
  params: {
    applicationType: 'web'
    disableLocalAuth: true
    location: location
    name: applicationInsightsName
    retentionInDays: 90
    roleAssignments: [
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Monitoring Metrics Publisher'
      }
    ]
    tags: tags
    workspaceResourceId: logAnalytics.outputs.resourceId
  }
}

module storage 'br/public:avm/res/storage/storage-account:0.32.0' = {
  name: 'storage'
  params: {
    allowBlobPublicAccess: false
    allowSharedKeyAccess: false
    blobServices: {
      containerDeleteRetentionPolicyDays: 7
      containerDeleteRetentionPolicyEnabled: true
      containers: [
        {
          name: deploymentStorageContainerName
          publicAccess: 'None'
        }
      ]
      deleteRetentionPolicyDays: 7
      deleteRetentionPolicyEnabled: true
    }
    defaultToOAuthAuthentication: true
    kind: 'StorageV2'
    location: location
    minimumTlsVersion: 'TLS1_2'
    name: storageAccountName
    publicNetworkAccess: 'Enabled'
    roleAssignments: [
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Storage Blob Data Owner'
      }
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Storage Blob Data Contributor'
      }
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Storage Queue Data Contributor'
      }
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Storage Table Data Contributor'
      }
    ]
    skuName: 'Standard_LRS'
    supportsHttpsTrafficOnly: true
    tags: tags
  }
}

module keyVault 'br/public:avm/res/key-vault/vault:0.13.3' = {
  name: 'keyVault'
  params: {
    enablePurgeProtection: enableKeyVaultPurgeProtection
    enableRbacAuthorization: true
    location: location
    name: keyVaultName
    publicNetworkAccess: 'Enabled'
    roleAssignments: [
      {
        principalId: userAssignedIdentity.outputs.principalId
        principalType: 'ServicePrincipal'
        roleDefinitionIdOrName: 'Key Vault Secrets Officer'
      }
    ]
    secrets: [
      {
        name: teslaClientSecretSecretName
        value: teslaClientSecret
      }
      {
        name: teslaPublicKeySecretName
        value: teslaPublicKeyPem
      }
      {
        name: oauthStateHmacKeySecretName
        value: oauthStateHmacKey
      }
    ]
    sku: 'standard'
    softDeleteRetentionInDays: keyVaultSoftDeleteRetentionInDays
    tags: tags
  }
}

module appServicePlan 'br/public:avm/res/web/serverfarm:0.7.0' = {
  name: 'appServicePlan'
  params: {
    kind: 'functionapp'
    location: location
    name: appServicePlanName
    reserved: true
    skuName: 'FC1'
    tags: tags
    zoneRedundant: false
  }
}

module functionApp 'br/public:avm/res/web/site:0.22.0' = {
  name: 'functionApp'
  params: {
    basicPublishingCredentialsPolicies: [
      {
        allow: false
        name: 'ftp'
      }
      {
        allow: false
        name: 'scm'
      }
    ]
    configs: [
      {
        name: 'appsettings'
        properties: {
          APPLICATIONINSIGHTS_AUTHENTICATION_STRING: 'ClientId=${userAssignedIdentity.outputs.clientId};Authorization=AAD'
          APPLICATIONINSIGHTS_CONNECTION_STRING: applicationInsights.outputs.?connectionString ?? ''
          AZURE_CLIENT_ID: userAssignedIdentity.outputs.clientId
          AzureWebJobsStorage__accountName: storage.outputs.name
          AzureWebJobsStorage__blobServiceUri: blobServiceUri
          AzureWebJobsStorage__clientId: userAssignedIdentity.outputs.clientId
          AzureWebJobsStorage__credential: 'managedidentity'
          AzureWebJobsStorage__queueServiceUri: queueServiceUri
          AzureWebJobsStorage__tableServiceUri: tableServiceUri
          FUNCTIONS_EXTENSION_VERSION: '~4'
          FUNCTIONS_WORKER_RUNTIME: 'Custom'
          KEY_VAULT_URI: keyVault.outputs.uri
          OAUTH_STATE_HMAC_KEY: '@Microsoft.KeyVault(SecretUri=${keyVault.outputs.uri}secrets/${oauthStateHmacKeySecretName}/)'
          TESLA_CLIENT_ID: teslaClientId
          TESLA_CLIENT_SECRET: '@Microsoft.KeyVault(SecretUri=${keyVault.outputs.uri}secrets/${teslaClientSecretSecretName}/)'
          TESLA_PUBLIC_KEY_PEM: '@Microsoft.KeyVault(SecretUri=${keyVault.outputs.uri}secrets/${teslaPublicKeySecretName}/)'
          TESLA_REDIRECT_URI: teslaRedirectUri
          TESLA_REFRESH_TOKEN_SECRET_NAME: teslaRefreshTokenSecretName
          TESLA_REGION: teslaRegion
          TESLA_SCOPE: teslaScope
        }
      }
    ]
    functionAppConfig: {
      deployment: {
        storage: {
          authentication: {
            type: 'UserAssignedIdentity'
            userAssignedIdentityResourceId: userAssignedIdentity.outputs.resourceId
          }
          type: 'blobContainer'
          value: deploymentStorageContainerUri
        }
      }
      runtime: {
        name: 'custom'
      }
      scaleAndConcurrency: {
        instanceMemoryMB: instanceMemoryMB
        maximumInstanceCount: maximumInstanceCount
      }
    }
    httpsOnly: true
    keyVaultAccessIdentityResourceId: userAssignedIdentity.outputs.resourceId
    kind: 'functionapp,linux'
    location: location
    managedIdentities: {
      userAssignedResourceIds: [
        userAssignedIdentity.outputs.resourceId
      ]
    }
    name: functionAppName
    publicNetworkAccess: 'Enabled'
    serverFarmResourceId: appServicePlan.outputs.resourceId
    siteConfig: {
      alwaysOn: false
      ftpsState: 'Disabled'
      minTlsVersion: '1.2'
    }
    tags: tags
  }
}

resource customDomainBinding 'Microsoft.Web/sites/hostNameBindings@2024-11-01' = if (createCustomDomainBinding && !empty(customDomainName)) {
  name: '${functionAppName}/${customDomainName}'
  properties: {
    azureResourceName: functionAppName
    azureResourceType: 'Website'
    customHostNameDnsRecordType: 'CName'
    hostNameType: 'Verified'
    siteName: functionAppName
  }
}

output functionAppDefaultHostname string = functionApp.outputs.?defaultHostname ?? ''
output functionAppName string = functionApp.outputs.name
output functionAppResourceId string = functionApp.outputs.resourceId
output functionAppCustomDomainVerificationId string = functionApp.outputs.?customDomainVerificationId ?? ''
output deploymentStorageContainerName string = deploymentStorageContainerName
output deploymentStorageContainerUri string = deploymentStorageContainerUri
output keyVaultUri string = keyVault.outputs.uri
output userAssignedIdentityClientId string = userAssignedIdentity.outputs.clientId
output userAssignedIdentityPrincipalId string = userAssignedIdentity.outputs.principalId
output cloudflareCnameTarget string = functionApp.outputs.?defaultHostname ?? ''
output cloudflareTxtRecordName string = empty(customDomainName) ? '' : 'asuid.${customDomainName}'
output cloudflareTxtRecordValue string = functionApp.outputs.?customDomainVerificationId ?? ''
output customDomainBindingEnabled bool = createCustomDomainBinding && !empty(customDomainName)
