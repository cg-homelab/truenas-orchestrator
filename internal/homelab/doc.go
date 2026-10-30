// Package homelab is the I/O-free domain: the layout contract, env scopes, the validation
// pipeline, and the interfaces the domain consumes (TrueNAS, Repo, SecretStore, ComposeParser).
//
// It takes already-read content in and returns values and findings out. It must not import
// os, io/fs, net, net/http, database/sql or os/exec; depguard enforces that.
package homelab
