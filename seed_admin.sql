INSERT INTO users (email, display_name, password, role, created_at, updated_at)
VALUES (
  'joynerlee99@gmail.com',
  'joynerlee',
  '$2a$10$6mSdsQr5gKzD67P2w2Ujx.dm/38eL27yfr9mLIpsvQZ6v0a4LM28i',
  'administrator',
  NOW(),
  NOW()
)
ON CONFLICT (email) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  password = EXCLUDED.password,
  role = EXCLUDED.role,
  updated_at = NOW();
