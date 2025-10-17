-- Migration: drop old idx_name_method_user constraint
-- Created: Fri Oct 17 04:35:50 PM IST 2025
-- Description: drop old idx_name_method_user constraint
-- Type: UP (apply changes)

-- Drop the old constraint if it exists
ALTER TABLE cron_job_listing_dbs DROP CONSTRAINT IF EXISTS idx_name_method_user;

-- Drop the old unique index if it exists
DROP INDEX IF EXISTS idx_name_method_user;
