# Session Storage Providers

## Overview

OpenCode supports multiple database backends for session storage. By default, sessions are stored locally using SQLite. For team collaboration, you can use MySQL to share session history across machines.

## SQLite (Default)

Zero-configuration, file-based storage. Sessions are stored in the `data.directory` location (default: `~/.opencode/`).

```json
{
  "sessionProvider": {
    "type": "sqlite"
  }
}
```

## MySQL

MySQL enables centralized session storage for teams sharing session history across multiple machines.

### Configuration

**Using environment variables:**

```bash
export OPENCODE_SESSION_PROVIDER_TYPE=mysql
export OPENCODE_MYSQL_DSN="user:password@tcp(localhost:3306)/opencode?parseTime=true"
```

**Using config file with DSN:**

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "dsn": "user:password@tcp(localhost:3306)/opencode?parseTime=true"
    }
  }
}
```

**Using individual connection parameters:**

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "host": "localhost",
      "port": 3306,
      "database": "opencode",
      "username": "opencode_user",
      "password": "secure_password"
    }
  }
}
```

**Optional connection pool settings (defaults shown):**

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "dsn": "...",
      "maxConnections": 10,
      "maxIdleConnections": 5,
      "connectionTimeout": 30
    }
  }
}
```

### Setup

**Option 1: Docker Compose**

A `docker-compose.yml` file is provided for quick setup:

```bash
docker-compose up -d

export OPENCODE_SESSION_PROVIDER_TYPE=mysql
export OPENCODE_MYSQL_DSN="opencode_user:secure_password@tcp(localhost:3306)/opencode?parseTime=true"

opencode
```

**Option 2: Manual Setup**

```sql
CREATE DATABASE opencode CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'opencode_user'@'%' IDENTIFIED BY 'secure_password';
GRANT ALL PRIVILEGES ON opencode.* TO 'opencode_user'@'%';
FLUSH PRIVILEGES;
```

Migrations run automatically on first connection.

### Troubleshooting

**Connection errors:**
- Verify MySQL is running: `mysql -h localhost -u opencode_user -p`
- Check firewall rules allow connections to MySQL port
- Ensure credentials are correct in configuration

**Migration errors:**
- Check MySQL user has sufficient privileges (CREATE, ALTER, INDEX)
- Verify database exists and is accessible
- Check logs for detailed error messages

## Project Scoping

Sessions are automatically scoped by project to ensure isolation:

- **Git repositories** use the remote origin URL as project ID (e.g., `github.com/opencode-ai/opencode`)
- **Non-git directories** fall back to the base directory name (e.g., `my-app`)

This ensures teams working on the same repository share sessions when using MySQL, while different projects remain isolated.
