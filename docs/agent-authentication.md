# Agent authentication evidence

AO reports authentication separately from installation. Installing an agent does
not prove that its selected provider is usable.

- `authorized`: a supported native check validated authentication remotely.
- `configured`: local credentials or a credential source were found, but provider
  access has not been verified. The UI shows “Credentials found, unverified”.
- `unauthorized`: explicit negative evidence, such as a rejected login or an
  expired credential without a refresh path.
- `not_applicable`: the selected provider supports operation without credentials.
- `unknown`: AO cannot establish the selected configuration's authentication.

Configured and unknown agents remain selectable. Rechecking refreshes evidence;
it does not guarantee that a CLI exposing only local status can verify access.
Configuration checks use the supplied session workspace and provider/model where
available. Device-wide inventory cannot resolve every project-specific setup.

## Deliberate limits

These checks do not reproduce each CLI's complete configuration parser or cloud
SDK. They inspect known authentication fields and native status commands, not
unrelated model pricing, reasoning, or setup UI metadata. They do not execute
credential helpers, refresh tokens, or query cloud metadata endpoints. AWS
credential processes and metadata-only credentials, Google metadata-only ADC,
and complete Azure identity chains can therefore remain unknown. Local cloud
evidence is configuration, not proof of model entitlement.

Keyring coverage is platform and agent specific. Muse keychain-only storage still
needs a verified native selector; Linux Secret Service and Windows Credential
Manager coverage is incomplete. AO does not guess undocumented keychain entries.

Invocation-only credentials, unsupported configuration formats or precedence
rules, dynamically registered providers, and unsupported remote auth brokers can
remain unknown. The CLI is authoritative when its behavior differs. Additional
sources should be added with version-specific official evidence and focused
regression fixtures, rather than broad recursive searches for token-like fields.
