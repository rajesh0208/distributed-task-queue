-- Rollback initial schema

DROP INDEX IF EXISTS idx_users_api_key;
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_username;
DROP INDEX IF EXISTS idx_tasks_type;
DROP INDEX IF EXISTS idx_tasks_created_at;
DROP INDEX IF EXISTS idx_tasks_status;

DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS tasks;

