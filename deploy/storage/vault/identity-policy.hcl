# The Vault policy of the Identity Service. This file is the only copy: the e2e
# stack (tests/e2e/identity-vault-init.sh) and the keystore least-privilege
# test (src/identity/internal/keystore) read it, and a deployment's own Vault
# bootstrap should read it too rather than keep a copy. The KV_MOUNT
# placeholder below is replaced with the KV v2 mount name before the policy is
# written.

# Read, create and update one agent key.
path "${KV_MOUNT}/data/agents/*" {
  capabilities = ["create", "update", "read"]
}

# List the agents, list one agent's keys, and erase a key completely.
path "${KV_MOUNT}/metadata/agents/*" {
  capabilities = ["read", "list", "delete"]
}
