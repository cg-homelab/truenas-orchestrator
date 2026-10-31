package homelab

// NetworksApp is the name of the custom app that owns the homelab's external Docker networks (D33).
const NetworksApp = "homelab-networks"

// HomelabNetworks are the external Docker networks the homelab stacks use.
var HomelabNetworks = []string{"home", "proxy", "isolated", "databases"}

// NetworksAppCompose is the compose file of the NetworksApp app. deploy/homelab-networks.yaml is a
// byte-identical copy.
const NetworksAppCompose = `# Managed by truenas-orchestrator (D33): creates the external networks the homelab stacks use.
# Do not stop or delete this app while any stack is running; stopping it removes unused networks.
services:
  holder:
    image: busybox:1.37.0
    command: ["sleep", "infinity"]
    restart: unless-stopped
    read_only: true
    user: "65534:65534"
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    networks: [home, proxy, isolated, databases]
networks:
  home: {name: home, driver: bridge}
  proxy: {name: proxy, driver: bridge}
  isolated: {name: isolated, driver: bridge, internal: true}
  databases: {name: databases, driver: bridge}
`
