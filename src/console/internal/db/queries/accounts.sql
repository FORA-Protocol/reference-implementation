-- name: UpsertAccount :one
-- Records a sign-in. The provider is authoritative about the address, the
-- verified flag and the name, so each sign-in overwrites them; created_at is
-- left alone so the row keeps saying when this person first arrived.
INSERT INTO console.accounts (oidc_issuer, oidc_subject, email, email_verified, display_name)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (oidc_issuer, oidc_subject) DO UPDATE
   SET email          = EXCLUDED.email,
       email_verified = EXCLUDED.email_verified,
       display_name   = EXCLUDED.display_name,
       last_login_at  = NOW()
RETURNING *;
