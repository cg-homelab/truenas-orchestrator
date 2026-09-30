# Authelia
auth for your services

## Docker Compose Example

- [Official Documentation](https://www.authelia.com/overview/prologue/introduction/)
- [User File Reference](https://www.authelia.com/reference/guides/passwords/#yaml-format)
- [Traefik Integration](https://www.authelia.com/integration/proxies/traefik/)

## Prerequisites

1. Create a directory and files for Authelia data:
  ```bash
  mkdir -p ./authelia/config ./authelia/secrets
  touch ./authelia/config/configuration.yml
  touch ./authelia/config/users.yml
  touch ./authelia/config/notifications.yml
  touch ./authelia/secrets/JWT_SECRET
  touch ./authelia/secrets/SESSION_SECRET
  touch ./authelia/secrets/STORAGE_ENCRYPTION_KEY
  touch ./authelia/secrets/STORAGE_PASSWORD
  ```
2. Genarate secrets and add them to the respective files:
  ```bash
  head -c 32 /dev/urandom | base64 > ./authelia/secrets/JWT_SECRET
  head -c 64 /dev/urandom | base64 > ./authelia/secrets/SESSION_SECRET
  head -c 64 /dev/urandom | base64 > ./authelia/secrets/STORAGE_ENCRYPTION_KEY
  ```
  alternatively, you can use: 
  - [web generator 32 length](https://generate-secret.vercel.app/32)
  - [web generator 64 length](https://generate-secret.vercel.app/64)
3. Setup a database for Authelia (PostgreSQL or MySQL). Example for PostgreSQL:
  From cmd:
  ```bash
  docker exec db-pg psql -U postgres -c "CREATE USER authelia WITH PASSWORD 'your_password';"
  docker exec db-pg psql -U postgres -c "CREATE DATABASE authelia OWNER authelia;"
  ```

  From sql command:
  ```sql
  CREATE USER authelia WITH PASSWORD 'your_password';
  CREATE DATABASE authelia OWNER authelia;
  ```
4. Configure authelia by editing `./authelia/config/configuration.yml`. Example:
  ```yaml
  theme: dark
  log:
    level: 'info'

  server:
    endpoints:
      authz:
        forward-auth:
          implementation: 'ForwardAuth'

  storage:
    postgres:
      address: 'tcp://db-pg:5432'
      servers: []
      database: 'authelia'
      schema: 'public'
      username: 'authelia'
      timeout: '5s'

  password_policy:
    standard:
      enabled: false
      min_length: 8
      max_length: 0
      require_uppercase: false
      require_lowercase: false
      require_number: false
      require_special: false
    zxcvbn:
      enabled: false
      min_score: 3

  session:
    cookies:
      - domain: 'example.com'
        authelia_url: 'https://auth.example.com'
        default_redirection_url: 'https://dashboard.example.com'

  authentication_backend:
    refresh_interval: '5m'
    password_reset:
      disable: false
    password_change:
      disable: false
    file:
      path: '/config/users.yml'
      watch: false
      search:
        email: true
        case_insensitive: false
      extra_attributes:
        extra_example:
          multi_valued: false
          value_type: 'string'
      password:
        algorithm: 'argon2'
        argon2:
          variant: 'argon2id'
          iterations: 3
          memory: 65536
          parallelism: 4
          key_length: 32
          salt_length: 16

  access_control:
    default_policy: 'deny'
    rules:
    - domain: '*.example.com'
      policy: 'one_factor'
      subject:
      - ['group:admins']

  notifier:
    disable_startup_check: false
    filesystem:
      filename: '/config/notifications.txt'

  ```

## Generate a password
```bash
docker run --rm -it authelia/authelia:latest authelia crypto hash generate argon2
```
