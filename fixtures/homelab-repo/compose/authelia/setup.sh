#!/bin/bash

# Setup folders
mkdir -p "${APPDATA_STORAGE}/authelia/config" "${APPDATA_STORAGE}./authelia/secrets"

# Setup config files

cp ./config/configuration.yml "${APPDATA_STORAGE}/authelia/config/configuration.yml"
touch ${APPDATA_STORAGE}/authelia/config/users.yml
touch ${APPDATA_STORAGE}/authelia/config/notifications.yml
touch ${APPDATA_STORAGE}/authelia/secrets/JWT_SECRET
touch ${APPDATA_STORAGE}/authelia/secrets/SESSION_SECRET
touch ${APPDATA_STORAGE}/authelia/secrets/STORAGE_ENCRYPTION_KEY
touch ${APPDATA_STORAGE}/authelia/secrets/STORAGE_PASSWORD

# Generate secrets
head -c 32 /dev/urandom | base64 >./authelia/secrets/JWT_SECRET
head -c 64 /dev/urandom | base64 >./authelia/secrets/SESSION_SECRET
head -c 64 /dev/urandom | base64 >./authelia/secrets/STORAGE_ENCRYPTION_KEY

# Setup password
# TODO: Prompt user for password
AUTHELIA_DB_PASSWORD="PROMPT USER FOR THIS"

# TODO: user output of modify this for script use so user only type it in once

# Setup existing docker database
docker exec db-pg psql -U postgres -c "CREATE USER authelia WITH PASSWORD ${AUTHELIA_DB_PASSWORD};"
docker exec db-pg psql -U postgres -c "CREATE DATABASE authelia OWNER authelia;"

# docker run --rm -it authelia/authelia:latest authelia crypto hash generate argon2
