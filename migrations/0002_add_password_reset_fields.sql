-- Adds columns to store password reset token and expiry for users
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS password_reset_token varchar(512),
  ADD COLUMN IF NOT EXISTS password_reset_expiry timestamptz;

-- Optional index to lookup by token quickly (if needed)
CREATE INDEX IF NOT EXISTS idx_users_password_reset_token ON users (password_reset_token);
