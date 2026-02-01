-- Initialize OpenCode database with proper character set and user privileges
-- This script runs automatically when the MySQL container starts for the first time

-- Grant all privileges to opencode_user on opencode database
GRANT ALL PRIVILEGES ON opencode.* TO 'opencode_user'@'%';
FLUSH PRIVILEGES;
