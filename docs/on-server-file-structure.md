# On server file structure

The on server file structure relevant for this project

## Related evironment variables

- **$HOMELAB_STORAGE**: Path to homelab git directory on this server
- **$APPCONFIG_STORAGE**: Path to root folder for app configs

## Repository structure

- $HOMELAB_STORAGE/ - git repository on server representing source of truth
  - **stacks/** - Folder containing all truenas/compose apps
    - **example.global.env** - sourced from origin
    - **global.env** - local copy if exists
    - **app1/** - folder containing app spec
      - **compose.yaml** - yaml file used by trunas to run docker compose
      - **example.env** - sourced from origin
      - **.env** - local copy of example if exists
      - **config/** - optional directory that might contain templates for what should be in `${APPCONFIG_STORAGE}/app1/`
  - **ansible/** - folder containing ansible workbooks - currently not relevant for this project
- **$APPCONFIG_STORAGE/** - Location on server that that contains the config folders for the docker apps
