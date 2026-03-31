---
name: mysql-recover
description: Recover MySQL/InnoDB tables with missing tablespace (.ibd files) in Docker environments. Use when you see ERROR 1812 (HY000) "Tablespace is missing for table" after Docker restarts or crashes.
user-invocable: true
metadata:
  category: database
  audience: developers
---

# MySQL Tablespace Recovery

## What I do

- Diagnose missing InnoDB tablespace (`.ibd`) files after Docker restarts
- Recover table data from MySQL binary logs (binlogs)
- Recreate broken tables with the correct schema
- Replay row-level changes to restore data

## When to use me

Use this skill when you encounter:
```
ERROR 1812 (HY000): Tablespace is missing for table `<database>`.`<table>`
```

This typically happens when:
- A Docker container with a MySQL bind mount is restarted uncleanly
- The `.ibd` file for a table is deleted or corrupted
- InnoDB's data dictionary references a tablespace file that no longer exists on disk

## Prerequisites

Before starting, gather:
1. **Docker container name** running MySQL
2. **Host path** of the MySQL data bind mount
3. **Database name** and **table name** that are broken
4. **MySQL credentials** (check container env vars if unknown)

## Step-by-step recovery

### Step 1: Identify the container and credentials

```bash
# Find the MySQL container
docker ps -a --format "{{.ID}} {{.Names}} {{.Status}}" | grep -i mysql

# Get credentials from container environment
docker exec <container> env | grep -i mysql
```

### Step 2: Check if the .ibd file exists on disk

```bash
ls -la <host_mount_path>/<database>/
```

Look for `<table>.ibd`. If it exists and is non-zero, try the **import tablespace** approach first (see Alternative: Import Tablespace below). If it's missing, continue to Step 3.

### Step 3: Check for binary logs

```bash
ls -la <host_mount_path>/binlog.*
```

Binary logs are required for data recovery. If they don't exist (binary logging was disabled), data cannot be recovered — skip to Step 7.

### Step 4: Extract the table schema from information_schema

The broken table still has its schema in the data dictionary even though the tablespace is missing:

```bash
docker exec <container> mysql -u root -p<password> information_schema -e \
  "SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLUMN_DEFAULT, EXTRA \
   FROM COLUMNS WHERE TABLE_SCHEMA='<database>' AND TABLE_NAME='<table>' \
   ORDER BY ORDINAL_POSITION;"
```

Also get indexes:

```bash
docker exec <container> mysql -u root -p<password> information_schema -e \
  "SELECT INDEX_NAME, COLUMN_NAME, SEQ_IN_INDEX, NON_UNIQUE \
   FROM STATISTICS WHERE TABLE_SCHEMA='<database>' AND TABLE_NAME='<table>' \
   ORDER BY INDEX_NAME, SEQ_IN_INDEX;"
```

### Step 5: Drop the broken table and recreate it

Construct the `CREATE TABLE` statement from the schema info gathered above, then:

```bash
docker exec <container> mysql -u root -p<password> <database> -e "DROP TABLE <table>;"

docker exec <container> mysql -u root -p<password> <database> -e "
CREATE TABLE <table> (
  -- columns from Step 4
  PRIMARY KEY (id),
  -- indexes from Step 4
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
"
```

### Step 6: Replay binary logs

First, verify which binlogs contain data for the table:

```bash
# Count references per binlog (use mysqlbinlog on host if not available in container)
for f in <host_mount_path>/binlog.*[0-9]; do
  count=$(mysqlbinlog --database=<database> "$f" 2>/dev/null | grep -c "<table>")
  [ "$count" -gt 0 ] && echo "$(basename $f): $count"
done
```

Then replay all binlogs that contain data. Use `--force` to skip duplicate key errors on other tables:

```bash
mysqlbinlog --database=<database> \
  <host_mount_path>/binlog.000001 \
  <host_mount_path>/binlog.000002 \
  ... \
  2>/dev/null | docker exec -i <container> mysql -u root -p<password> --force <database>
```

**Important notes on replay:**
- Binlogs in ROW format (default in MySQL 8.0) don't show readable SQL — they use `Table_map` and `Write_rows` events. The replay still works correctly.
- `--force` causes duplicate key errors on other tables to be skipped. This is safe — those tables already have their data.
- Process binlogs **in order** (oldest to newest) to maintain correct data sequence.
- This can take several minutes for large binlogs.

### Step 7: Verify recovery

```bash
docker exec <container> mysql -u root -p<password> <database> -e \
  "SELECT COUNT(*) FROM <table>;"

# Spot-check recent rows
docker exec <container> mysql -u root -p<password> <database> -e \
  "SELECT * FROM <table> ORDER BY created_at DESC LIMIT 5;"
```

## Alternative: Import tablespace

If the `.ibd` file exists and is non-zero, try re-linking it before resorting to binlog replay:

```sql
ALTER TABLE <table> DISCARD TABLESPACE;
-- Copy the .ibd file back into the database directory if needed
ALTER TABLE <table> IMPORT TABLESPACE;
```

## Alternative: InnoDB force recovery

If many tables are broken or the server won't start, try recovery mode:

Add to MySQL startup (Docker Compose `command:` or environment):
```yaml
command: --innodb-force-recovery=1
```

Recovery levels 1-3 are read-only safe. Once MySQL starts:
1. Dump all data: `mysqldump -u root -p<password> --all-databases > backup.sql`
2. Remove recovery mode and restart
3. Reimport: `mysql -u root -p<password> < backup.sql`

## Finding mysqlbinlog

The `mysqlbinlog` utility may not be installed inside the Docker container. Check the host:

```bash
# macOS with Homebrew
which mysqlbinlog || find /opt/homebrew /usr/local -name "mysqlbinlog" 2>/dev/null

# Linux
which mysqlbinlog || dpkg -L mysql-client-core-* 2>/dev/null | grep mysqlbinlog
```

If not available, install it:
```bash
# macOS
brew install mysql-client

# Ubuntu/Debian
apt-get install mysql-client-core-8.0
```

## Prevention

- Always stop MySQL containers gracefully: `docker stop --time=30 <container>` (sends SIGTERM and waits)
- Avoid `docker kill` or `docker restart` without a grace period
- Enable binary logging (default in MySQL 8.0) — it's your recovery lifeline
- Consider periodic `mysqldump` backups via a cron job
- Use Docker volumes instead of bind mounts for better data integrity
