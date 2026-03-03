-- Rollback additional indexes

DROP INDEX IF EXISTS idx_workers_last_heartbeat;
DROP INDEX IF EXISTS idx_workers_status;
DROP INDEX IF EXISTS idx_tasks_status_created;
DROP INDEX IF EXISTS idx_tasks_worker_id;

