-- Migration: rename token_rev (int64 revision counter) to jwt_token (varchar storing bcrypt hash)
-- This supports the new JWT + refresh token auth flow where tokens are
-- validated against their stored hash and revoked by clearing the column.

-- Drop the old integer column and add the new varchar column
ALTER TABLE users DROP COLUMN IF EXISTS token_rev;
ALTER TABLE users ADD COLUMN IF NOT EXISTS jwt_token VARCHAR(512) DEFAULT '';
