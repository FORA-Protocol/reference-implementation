#!/usr/bin/env sh
# e2e only: gives the Identity Service a scoped Vault token instead of root.
# Runs once in the identity-vault-init container, after the dev-mode Vault is
# healthy and before the Identity Service starts. The Identity Service never
# gets the root token. Besides identity-vault, which defines it, only this
# container and the test runner hold it; the runner uses it to arrange and
# check state the scoped token must not reach.
#
#   1. writes the fora-identity policy, rendered from the shared template
#      deploy/storage/vault/identity-policy.hcl for IDENTITY_KV_MOUNT;
#   2. creates a token with the fixed id IDENTITY_VAULT_TOKEN_ID that carries
#      only that policy. -no-default-policy leaves out Vault's "default"
#      policy, so the token cannot even look itself up or renew itself; the
#      TTL is 768 h (the dev server's maximum), far longer than any e2e run.
#      Vault warns that a custom id is hashed with SHA1; that is acceptable
#      for a throwaway e2e Vault;
#   3. looks the token up with the root token and fails unless its policies
#      are exactly [fora-identity].
#
# Any failure exits non-zero, and the Identity Service, which depends on this
# container completing successfully, does not start.
#
# Inputs (env): VAULT_ADDR, VAULT_TOKEN (root), IDENTITY_KV_MOUNT,
# IDENTITY_VAULT_TOKEN_ID, and the template mounted at /identity-policy.hcl.

set -eu

: "${VAULT_ADDR:?}" "${VAULT_TOKEN:?}" "${IDENTITY_KV_MOUNT:?}" "${IDENTITY_VAULT_TOKEN_ID:?}"
POLICY_NAME=fora-identity

sed "s/\${KV_MOUNT}/${IDENTITY_KV_MOUNT}/g" /identity-policy.hcl | vault policy write "${POLICY_NAME}" -

# A second `compose up` against the same Vault finds the token already there;
# a token id cannot be created twice, so the old token is revoked first.
if vault token lookup "${IDENTITY_VAULT_TOKEN_ID}" >/dev/null 2>&1; then
    vault token revoke "${IDENTITY_VAULT_TOKEN_ID}" >/dev/null
fi
vault token create -id="${IDENTITY_VAULT_TOKEN_ID}" -policy="${POLICY_NAME}" \
    -no-default-policy -ttl=768h >/dev/null

# The image has no jq: squeeze the JSON and cut out the policies array.
lookup="$(vault token lookup -format=json "${IDENTITY_VAULT_TOKEN_ID}")"
policies="$(printf '%s' "${lookup}" | tr -d ' \n' | grep -o '"policies":\[[^]]*\]')"
if [ "${policies}" != "\"policies\":[\"${POLICY_NAME}\"]" ]; then
    echo "identity-vault-init: the Identity Service token has ${policies}, want exactly [${POLICY_NAME}]" >&2
    exit 1
fi
echo "identity-vault-init: Identity Service token created with policies ${policies}"
