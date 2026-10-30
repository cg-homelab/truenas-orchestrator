# Go guidelines

Normative. Every Go file in this repository follows these rules, and `just check` enforces the ones a
machine can check. Written for a reader who knows Rust well and Go less well, and for AI coding agents.
The short version for agents is the root [`CLAUDE.md`](../../CLAUDE.md); this document is the full text.

Layout and package rationale: [components.md](components.md). Decisions behind the choices: D15–D28 in
[decisions.md](../decisions.md).

---

## 1. Sources of truth

In order of precedence:

1. **This document.** Where it disagrees with a source below, it wins and says why at that point.
2. **[Google Go Style Guide](https://google.github.io/styleguide/go/)** — the Guide, Decisions and Best
   Practices documents. The normative baseline (D27).
3. **[Effective Go](https://go.dev/doc/effective_go)** and
   **[Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)** — required reading before the
   first change.
4. **[Uber Go Style Guide](https://github.com/uber-go/guide)** — supplementary. Useful for concrete
   examples; ignore its Uber-specific library advice (zap, multierr).

Architecture references:

- Ben Johnson, "Standard Package Layout" — the domain package owns the types and the interfaces it
  consumes; each dependency gets one package that implements them.
- Mat Ryer, ["How I write HTTP services in Go after 13 years"](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/)
  — `run()`, `NewServer`, `routes.go`, handler constructors.
- [go.dev/doc/modules/layout](https://go.dev/doc/modules/layout) — the official module layout guide.
  The `golang-standards/project-layout` repository is **not** a reference: it is not a Go team standard
  and its `pkg/` convention is rejected below.

---

## 2. Module and package layout

One module, `github.com/cg-homelab/truenas-orchestrator`, one binary.

- `cmd/orchestrator` — `main`, `run`, per-subcommand config, subcommand wiring and CLI output
  formatting, all tested through `run()`. No domain logic.
- `internal/<pkg>` — everything else. There is no `pkg/`: nothing here is a library for other modules.
- One package per dependency boundary. The six packages and what each may import:

```
cmd/orchestrator ──> every internal package (constructs and wires them)
internal/server  ──> homelab, compose, gitrepo, truenas, sqlite
internal/compose ──> homelab        (no I/O)
internal/gitrepo ──> homelab
internal/truenas ──> homelab
internal/sqlite  ──> homelab
internal/homelab ──> nothing in this module (no I/O)
```

Implementation packages never import each other; `server` and `cmd/orchestrator` compose them. An
import that points the other way is a design error.

**The validation pipeline lives in `internal/homelab`.** gitrepo reads, compose parses, homelab checks —
and since `homelab` cannot import `compose`, it declares the parser it consumes:

```go
// internal/homelab
type ComposeParser interface {
	Parse(path string, src []byte) (ComposeFile, error)
}

func ValidateRepo(ctx context.Context, repo Repo, parse ComposeParser, opts Options) (Report, error)

// internal/compose: *compose.Parser satisfies it
var _ homelab.ComposeParser = (*Parser)(nil)
```

`cmd/orchestrator`'s `validate` and `internal/server` both call `homelab.ValidateRepo` with the real
`*gitrepo.Repo` and `*compose.Parser`; neither re-implements the pipeline. `ComposeParser` is an explicit
exception to "no interface until there is a fake or a second implementation" (section 3.3): it exists so
the pipeline is unit-tested with an in-memory fake `Repo` and the real parser.

**`internal/homelab` and `internal/compose` are I/O-free.** They take bytes and values in and return values
and findings out. The `depguard` rule `domain-no-io` (section 9) fails the build if either imports `os`,
`io/fs`, `net`, `net/http`, `database/sql` or `os/exec`. Tests in those packages are exempt so they can
read `testdata/`.

Naming:

- Package names are short, lowercase, one word, and name what the package provides: `homelab`,
  `gitrepo`, `sqlite`. Never `util`, `common`, `types`, `helpers`, `models`, `base`.
- Do not repeat the package in exported names: `truenas.Client`, not `truenas.TrueNASClient`.
- Files are named for what they hold (`findings.go`, `envscope.go`), not `utils.go`.

Tests live beside the code (`envscope.go`, `envscope_test.go`), in the same package unless they test only
the exported API. Test inputs and golden outputs go in a `testdata/` directory next to the test, which
the go tool ignores. The shared fixture homelab repo is `fixtures/homelab-repo/`. `internal/testutil/` is
allowed for the golden-file helper only — it is not a place for shared test plumbing.

---

## 3. Rust → Go translation table

| Rust habit | Go way |
|---|---|
| `Result<T, E>`, `?` | `(T, error)`, `if err != nil { return …, fmt.Errorf("…: %w", err) }` |
| `enum` + `match` | typed constants + `switch`, checked by the `exhaustive` linter |
| trait | small interface, declared by the consumer |
| `Option<T>` | zero value, `v, ok :=`, or a pointer when absence must be told apart from zero |
| generics for abstraction | interfaces; generics only for containers and algorithms |
| `lazy_static` / global state | constructed in `run()`, passed explicitly |
| cancellation tokens | `context.Context` as the first parameter |
| `thiserror` enums | sentinel `Err…` vars, typed errors when callers read fields, `%w` wrapping |
| `Send`/`Sync`, the borrow checker | discipline, `sync.Mutex`, and `-race` in every CI run |
| builders, `get_`/`set_` | struct literals with field names, exported fields |

Each row in detail. The "Rust habit" is the Go a Rust programmer writes on day one.

### 3.1 Results are `(T, error)`

```go
// Rust habit: a Result type.
type Result[T any] struct { Val T; Err error }
func LoadEnv(b []byte) Result[Env]

// Go way: two return values. On error, the other values are zero and must not be read.
func LoadEnv(b []byte) (Env, error)
```

### 3.2 Enums are typed constants; finding codes are strings

```go
// Rust habit: iota ints, a String() method, and a default branch to keep the compiler quiet.
type Status int
const ( Ready Status = iota; NeedsSetup; Broken )
switch s { case Ready: …; default: … } // a new value silently falls into default

// Go way: string-typed constants (stable in JSON and logs), no default branch, exhaustive lint.
type Status string

const (
	StatusReady      Status = "Ready"
	StatusNeedsSetup Status = "NeedsSetup"
	StatusBroken     Status = "Broken"
)
```

`exhaustive` runs with `default-signifies-exhaustive: false`, so a `switch` on `Status`, `Severity` or
`Scope` must list every value — the `match` guarantee, restored by lint.

Finding codes are `type Code string` constants in `internal/homelab`, one per row of
[validator-checks.md](../validator-checks.md):

```go
// Code identifies a validator check. Values are stable: never renumber, never reuse.
type Code string

const (
	COMPOSE001 Code = "COMPOSE001"
	ENV003     Code = "ENV003"
	// ...one per registry row
)
```

**Deviation from the Google guide:** constants are normally MixedCaps (`Env003`). Codes are spelled exactly
as in the registry so that `grep -rn ENV003` finds the doc, the check and the tests in one go.

### 3.3 Traits become small interfaces at the consumer

```go
// Rust habit: one big interface beside its implementation, declared before anything needs it.
package truenas
type TrueNASAPI interface { /* forty methods */ }

// Go way: the consumer declares the methods it calls. Accept interfaces, return structs.
package homelab
type TrueNAS interface {
	Dataset(ctx context.Context, name string) (Dataset, error)
	CreateDataset(ctx context.Context, spec DatasetSpec) error
}

package truenas
func NewClient(url string, key homelab.Secret) *Client // concrete type out
var _ homelab.TrueNAS = (*Client)(nil)                  // compile-time check
```

**No interface until there is a fake or a second implementation.** Everything else is a concrete type.
The interfaces `internal/homelab` declares, and what justifies each from day one:

- `TrueNAS` — `*truenas.Client` and `truenas.Fake`.
- `SecretStore` — D5 requires the seam so an encrypted store can replace plaintext files without touching
  call sites.
- `Repo` and `ComposeParser` — the validation pipeline (section 2) is tested against an in-memory fake
  `Repo`.

### 3.4 `Option<T>` is mostly the zero value

```go
// Rust habit: an Option type, or a pointer for every optional field.
type Option[T any] struct { v T; some bool }
type Prompt struct { Label *string; Secret *bool }

// Go way: design the zero value to mean "absent"; use comma-ok; use a pointer only when
// zero is a valid value that must be told apart from "not set".
type Prompt struct { Label string; Secret bool }
func (e Env) Lookup(key string) (string, bool)
type Dir struct { Path string; Mode *uint32 } // mode 0 is valid, so "unset" needs the pointer
```

### 3.5 Generics are for containers and algorithms

```go
// Rust habit: generic structs for dependency injection, iterator-adapter chains.
type Validator[R Repo, S SecretStore] struct { repo R; secrets S }
errs := Filter(Map(findings, toErr), isError)

// Go way: interface fields; a plain loop. slices/maps packages for the rest.
type Validator struct { repo Repo; secrets SecretStore }
var errs []Finding
for _, f := range findings {
	if f.Severity == SeverityError {
		errs = append(errs, f)
	}
}
```

Return slices by default. Return an `iter.Seq[T]` (range-over-func) only when laziness matters, such as
walking a large tree with early exit.

### 3.6 No `init()`, no package-level mutable state

```go
// Rust habit: a global, initialised once.
var db *sql.DB
func init() { db, _ = sql.Open("sqlite", os.Getenv("DB_PATH")) }

// Go way: constructed in run(), passed to whoever needs it.
store, err := sqlite.Open(ctx, cfg.DatabasePath)
srv := server.NewServer(server.Deps{Store: store, Logger: logger})
```

Allowed at package level: constants, sentinel errors, compiled regexps, `//go:embed` variables, and the
`-update` test flag. Nothing that changes after start-up.

### 3.7 `context.Context` is the first parameter, never a field

```go
// Rust habit: hold the cancellation handle in the struct.
type Fetcher struct { ctx context.Context; repo string }

// Go way: every call that blocks, does I/O or may be cancelled takes ctx first.
func (r *Repo) Fetch(ctx context.Context) error
```

Use `exec.CommandContext`, `http.NewRequestWithContext`, `db.QueryContext` — the `noctx` linter flags the
context-free variants. Never pass `context.Background()` below `run()`; in tests use `t.Context()`.

### 3.8 Errors are values

```go
// Rust habit: bare `?`, unwrap(), or a "failed to" chain with no inspectable cause.
data, _ := os.ReadFile(p)
if err != nil { return err }
if err != nil { panic(err) }
return errors.New("failed to load stack: " + err.Error())

// Go way: wrap with context and %w; lowercase, no trailing punctuation, no "failed to".
if err != nil {
	return Stack{}, fmt.Errorf("load stack %s: %w", name, err)
}
```

- **Sentinels** for conditions callers branch on: `var ErrNotFound = errors.New("not found")` in
  `internal/homelab`, alongside `ErrValidation`, `ErrConflict`, `ErrForbidden`, `ErrUnauthenticated`.
  Implementation packages translate their own errors into these at the boundary (`sql.ErrNoRows` →
  `homelab.ErrNotFound`).
- **Typed errors** only when callers need fields, such as `*truenas.RPCError{Code int; Message string}`.
- **Inspect with `errors.Is` / `errors.As`**, never `==` or a type assertion (`errorlint` enforces this).
- **Never return a typed nil pointer as `error`.** `var e *RPCError; return e` is a non-nil `error`.
- **No panics across a package boundary.** A panic means a programmer bug. `Must…` helpers only for
  constant inputs at start-up.

### 3.9 Concurrency: owned goroutines, mutexes for state

```go
// Rust habit: channels as the default for shared state (an mpsc "actor"), fire-and-forget spawns.
go s.refreshLoop() // who stops this?

// Go way: the code that starts a goroutine stops it; errgroup ties lifetimes to ctx.
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return serveHTTP(ctx, srv) }) // shuts srv down when ctx is done
g.Go(func() error { return jobs.Run(ctx) })       // returns when ctx is done
return g.Wait()                                   // first error cancels ctx for the rest
```

- Protect shared state with a `sync.Mutex` next to the data it guards. Use channels to hand off work or
  signal completion, not to emulate a lock.
- `sync.WaitGroup.Go` (Go 1.25) for fan-out without errors; `errgroup` when errors matter.
- There is no borrow checker. Slices alias their backing array, maps are shared by reference, and a struct
  holding a `sync.Mutex` must not be copied (`govet`'s `copylocks` catches that one). **`-race` is your
  borrow checker**, and CI always runs it.

### 3.10 No getters, setters or builders

```go
// Rust habit:
srv := NewServerBuilder().WithLogger(l).WithStore(s).Build()
port := cfg.GetPort()

// Go way: a struct literal with field names; exported fields; a getter, if needed, is Port().
srv := server.NewServer(server.Deps{Logger: l, Store: s})
port := cfg.Port
```

Always name fields in composite literals of another package's struct (`govet` `composites`). A
constructor takes required dependencies; use a `Deps` or `Options` struct once there are more than three.

---

## 4. HTTP service pattern

### 4.1 `main`, `run`, `NewAPI` and `NewServer`

```go
// cmd/orchestrator/main.go
func main() {
	if err := run(context.Background(), os.Args, os.Environ(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err)) // 1 for error findings, 2 for usage
	}
}

func run(ctx context.Context, args []string, environ []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) < 2 {
		return errUsage
	}
	switch cmd, rest := args[1], args[2:]; cmd {
	case "serve":
		return runServe(ctx, rest, environ, stdout)
	case "validate":
		return runValidate(ctx, rest, stdout) // flags and a path only: no environment, no database
	case "openapi":
		return runOpenAPI(stdout) // no configuration at all
	default:
		return fmt.Errorf("unknown command %q: %w", cmd, errUsage)
	}
}

// serveConfig is what `serve` needs; github.com/caarlos0/env/v11 fills it from environ.
type serveConfig struct {
	Listen         string `env:"ORCH_LISTEN" envDefault:":8080"`
	DatabasePath   string `env:"ORCH_DB_PATH,required"`
	HomelabStorage string `env:"HOMELAB_STORAGE,required"`
	SecureCookies  bool   `env:"ORCH_SECURE_COOKIES" envDefault:"true"`
}

func runServe(ctx context.Context, args, environ []string, stdout io.Writer) error {
	var cfg serveConfig
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: env.ToMap(environ)}); err != nil {
		return fmt.Errorf("load serve config: %w", err)
	}
	// open the store, build server.Deps, server.NewServer, listen until ctx is done
}

func runOpenAPI(stdout io.Writer) error {
	api := server.NewAPI(http.NewServeMux(), server.Deps{})
	b, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal openapi: %w", err)
	}
	_, err = stdout.Write(b)
	return err
}
```

`run` dispatches on the subcommand first, and each subcommand parses only the configuration it needs:
`serve` requires a database path, `validate` (which has no database in M1) takes flags and a path, and
`openapi` takes nothing. `main` is the only place that touches `os.Args`, `os.Environ`, `os.Stdout` and
`os.Exit`; nothing calls `os.Getenv`. Tests call `run()` directly with fake arguments, a literal
environment such as `[]string{"ORCH_DB_PATH=" + filepath.Join(t.TempDir(), "o.db"), "HOMELAB_STORAGE=/x"}`,
and a `bytes.Buffer`.

```go
// internal/server/server.go

// NewAPI registers every operation on mux. It must not call deps, so `orchestrator openapi`
// can build it with zero-value Deps.
func NewAPI(mux *http.ServeMux, deps Deps) huma.API {
	cfg := huma.DefaultConfig("truenas-orchestrator", deps.Version)
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", "" // spec comes from `orchestrator openapi`
	cfg.CreateHooks = nil                                       // no $schema links to the routes just removed
	api := humago.New(mux, cfg)
	api.UseMiddleware(authenticate(api, deps)) // before any Register: huma binds middleware at registration
	addRoutes(mux, api, deps)
	return api
}

func NewServer(deps Deps) http.Handler {
	mux := http.NewServeMux()
	NewAPI(mux, deps)
	var h http.Handler = mux
	h = logRequests(deps.Logger)(h)
	h = recoverPanics(deps.Logger)(h)
	return h
}
```

- **All routes are in one file, `internal/server/routes.go`**, in one function `addRoutes`. Reading it
  answers "what does this server expose".
- **Handlers are constructed by functions that take their dependencies** and return the handler as a
  closure: `handleGetStack(logger, repo)`. No server struct with twenty fields.
- **No hand-rolled JSON.** Ryer's `encode`/`decode` helpers are absent on purpose: huma's input and output
  structs replace them. Handlers outside huma (the SPA) serve files, never JSON.
- **Middleware constructors return `func(http.Handler) http.Handler`**: `logRequests(logger)(h)`.
  Cross-cutting concerns (logging, panic recovery, security headers) are stdlib middleware.
  Authentication is a huma middleware, because it reads each operation's declared `Security`.
- **`api.UseMiddleware(auth)` runs before the first `huma.Register`.** huma captures the middleware stack
  when an operation is registered; an operation registered earlier is silently unauthenticated.
- **huma's built-in `/openapi.json`, `/docs` and `/schemas` routes are disabled.** They would sit outside
  the auth middleware and shadow SPA paths. The document comes from `orchestrator openapi`.
- `NewAPI` must not call its dependencies; `runOpenAPI` passes zero-value `Deps`.
- Routing is the stdlib `ServeMux` with method and wildcard patterns (`GET /api/stacks/{name}`, Go 1.22).
  The SPA is a `fs.FS` in `Deps`, served by `http.FileServerFS` on `GET /` with a fallback to
  `index.html`; unknown `/api/` paths return 404, never the SPA.

### 4.2 huma operations

```go
// internal/server/stacks.go
type GetStackInput struct {
	Name string `path:"name" pattern:"^[a-z0-9-]+$" doc:"App directory name"`
}

type GetStackOutput struct {
	Body StackView
}

var opGetStack = huma.Operation{
	OperationID: "get-stack",
	Method:      http.MethodGet,
	Path:        "/api/stacks/{name}",
	Summary:     "Get one stack with its findings",
	Tags:        []string{"stacks"},
}

func handleGetStack(
	logger *slog.Logger, repo homelab.Repo,
) func(context.Context, *GetStackInput) (*GetStackOutput, error) {
	return func(ctx context.Context, in *GetStackInput) (*GetStackOutput, error) {
		st, err := repo.Stack(ctx, in.Name)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &GetStackOutput{Body: newStackView(st)}, nil
	}
}
```

- **OperationID** is kebab-case, verb first (`get-stack`, `list-stacks`, `fetch-repo`), unique and stable.
  It keys the `operations` type that `openapi-typescript` generates, which the frontend's typed wrappers
  are named after; renaming one is an API change.
- Input structs are `<Operation>Input`, output structs `<Operation>Output` with the response in `Body`.
  Validation lives in struct tags so it appears in the spec.
- Response bodies are view types defined in `internal/server` (`StackView`), never domain structs or
  sqlc rows — the JSON shape is a contract, the domain types are not.
- **Job progress is SSE** via huma's `sse.Register`, with one event type per message kind
  (`map[string]any{"progress": JobProgress{}, "done": JobResult{}}`) and `send.Data(...)` per event.

Domain errors map to HTTP status in exactly one function, `apiError`:

| Domain error | Status | huma constructor |
|---|---|---|
| `homelab.ErrNotFound` | 404 | `huma.Error404NotFound` |
| `homelab.ErrValidation` | 422 | `huma.Error422UnprocessableEntity` |
| `homelab.ErrConflict` | 409 | `huma.Error409Conflict` |
| `homelab.ErrForbidden` | 403 | `huma.Error403Forbidden` |
| `homelab.ErrUnauthenticated` | 401 | `huma.Error401Unauthorized` |
| anything else | 500 | `huma.Error500InternalServerError("internal error")` |

A 500 is logged with the full error and returned with a fixed message: internal error text can contain
paths and values and never reaches a client.

### 4.3 The OpenAPI contract

`orchestrator openapi` writes the document. `just generate` pipes it through `openapi-typescript` into
`frontend/src/lib/api.d.ts`. `just check-api` generates the same file into a temporary directory and
diffs it against the committed copy, so any route change that is not regenerated and committed fails CI
without `check` writing to the tree. The frontend calls the API only through `openapi-fetch` with
those types.

### 4.4 Sessions and tokens (D25)

- Session and API tokens are 32 bytes from `crypto/rand`. The database stores only their SHA-256 hash;
  the raw token exists in the cookie or is shown once at creation.
- Cookie: `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` from `serveConfig.SecureCookies`
  (`ORCH_SECURE_COOKIES`, default `true`). TLS terminates at Traefik, so `r.TLS` is always nil and cannot
  decide it; set `false` only for plain-HTTP local dev. A test asserts the flag follows the config.
- A new session id is issued on every login; logout deletes the row.
- Sessions expire on an idle timeout and an absolute lifetime, both from `serveConfig` (defaults 24 h and
  7 days). Expiry is checked on every request against the database, not the cookie.
- Login failures return one identical 401 for unknown user and wrong password, behind a per-IP
  `x/time/rate` limiter.
- State changes are never `GET`. Each of these rules has a test.

---

## 5. Data access

- Driver: `modernc.org/sqlite`, pure Go, registered as `"sqlite"`. Release builds use `CGO_ENABLED=0`.
- No ORM. SQL is written by hand in `internal/sqlite/queries/*.sql` and compiled to typed Go by `sqlc`.
  Generated files are committed and never edited.
- Migrations: `internal/sqlite/migrations/NNNN_name.sql` (`0001_init.sql`, `0002_sessions.sql`), each with
  `-- +goose Up` and `-- +goose Down` sections, embedded with `embed.FS` and applied at start-up. sqlc
  reads the same files as its schema. A migration that has shipped is never edited; add a new one.
- `just generate` (`go generate ./...`) is the only command that writes generated code. `just check`
  runs `go tool sqlc diff`, which compares what sqlc would generate with the committed files without
  writing, and fails on any difference.
- sqlc row types do not leave `internal/sqlite`. Its exported methods return `homelab` types, and
  `sql.ErrNoRows` becomes `homelab.ErrNotFound` there.

```sql
-- internal/sqlite/queries/jobs.sql
-- name: GetJob :one
SELECT id, kind, state, created_at FROM jobs WHERE id = ?;
```

Pragmas are set in the connection string so every pooled connection gets them:

```go
// error checks elided
dsn := "file:" + path +
	"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
db, err := sql.Open("sqlite", dsn)

//go:embed migrations/*.sql
var migrations embed.FS

sub, err := fs.Sub(migrations, "migrations")
p, err := goose.NewProvider(goose.DialectSQLite3, db, sub) // no goose globals
_, err = p.Up(ctx)
```

Transactions go through one unexported helper; nothing else calls `BeginTx`. Exported `Store` methods
use it internally, so sqlc's `*Queries` never leaves the package:

```go
func (s *Store) withTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}
```

### Parsing repo files

`internal/homelab` and `internal/compose` parse bytes they are handed; each format has one library.

- **TOML (`setup.toml`):** `github.com/pelletier/go-toml/v2` with `Decoder.DisallowUnknownFields()`.
  An unknown key is a hard error, `SETUP002`, and `errors.As(err, &strictErr)` with
  `*toml.StrictMissingError` gives the key path (`[app bogus]`) and line for the finding.
- **YAML:** `go.yaml.in/yaml/v3`, the YAML organisation's maintained continuation of yaml.v3 (the
  original `gopkg.in/yaml.v3` repository is archived). Positions come from walking `yaml.Node`, which
  carries the line of every key and scalar. compose-go already depends on the v4 release candidate of
  the same lineage; move to v4 when it is final.
- **Compose structure (`COMPOSE001`):** `compose-go/v2/schema.Validate` on the raw YAML decoded into a
  `map[string]any`. **Never the compose-go loader:** with interpolation disabled it fails on `${VAR}` in
  typed fields such as `ports` and `mem_limit`, and with its defaults it stats `env_file:` paths on disk.
- **Substitution grammar:** our own scanner in `internal/homelab/interpolate.go` (`${VAR:-d}`,
  `${VAR:?e}`, `$VAR`, `$$`), differentially tested against `compose-go/v2/template`, which is not
  called at runtime ([D29](../decisions.md)).

---

## 6. Logging and secrets

- **`log/slog` only.** The `*slog.Logger` is a dependency like any other: built in `run()`, passed in
  `Deps`. No global logger (`sloglint` `no-global: all`).
- Static messages, lowercase, with structured keys in `snake_case`:
  `logger.InfoContext(ctx, "repo fetched", "app_count", n, "duration", d)`. Never `fmt.Sprintf` a message.
- Use the `…Context` variants whenever a `ctx` is in scope, so request ids reach the log.
- **Never `fmt.Println` or `log.Print` in library code.** CLI output goes to the `stdout` writer that
  `run()` received.

Every secret value is a `homelab.Secret` from the moment it is read:

```go
// Secret holds a sensitive value. Every way of printing or encoding it redacts.
type Secret struct{ value *string }

func NewSecret(v string) Secret { return Secret{value: &v} }

// Reveal is the only way out; grep for it in review.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

func (s Secret) Format(f fmt.State, _ rune)   { fmt.Fprint(f, "[REDACTED]") }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
```

A struct, not `type Secret string`: a string type converts with `string(s)` and leaks through every API
that takes a string. `Format` rather than `String`/`GoString` because it covers every verb (`%d` on a
`String`-only type prints the raw field). The pointer matters when a `Secret` sits in an unexported field
of another struct: `fmt` cannot call methods there and prints the field raw, which is then an address,
not the value; `slog`'s handlers behave the same. Writing a secret is one explicit `Reveal()` at the point
of use.

- **`.env` values are redacted in every API response by default.** Revealing one is a separate,
  authenticated operation that writes an audit entry.
- **The audit log records that a secret was written, never the value:** `action=secret.write`,
  `target=authelia/.env:POSTGRES_PASSWORD`.
- Finding messages and error strings never interpolate a `.env` value. Name the key and the expected
  shape: `ENV007: METRICS_RETENTION_TIME is not a duration (e.g. 60d)`.
- `sloglint` forbids the keys `password`, `token`, `secret` and `api_key` (section 9).

---

## 7. Testing

- Table-driven tests with `t.Run`; subtest names are short readable phrases
  (`"var only in env_file used in volumes"`).
- Compare with `cmp.Diff(want, got)` from `github.com/google/go-cmp`, using `cmpopts` for ordering and
  unexported fields. Failure messages follow `Func(args) = got, want want` and `(-want +got)`.
- No assertion libraries, no mock generators. **Hand-written fakes:** `truenas.Fake` is a real
  implementation of `homelab.TrueNAS` backed by a map and recorded fixtures from `fixtures/truenas/`.
- `t.TempDir()` for files, `t.Context()` for contexts, `t.Setenv` never (pass `environ` to `run()` instead).
- Time-driven code (scheduler, session expiry) is tested in a `testing/synctest` bubble (Go 1.25), where
  the clock is fake and `time.Sleep` returns instantly:

```go
func TestSchedulerFiresDaily(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fired := make(chan struct{}, 1)
		go NewScheduler(24*time.Hour, func(context.Context) { fired <- struct{}{} }).Run(ctx)
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		select {
		case <-fired:
		default:
			t.Fatal("job did not fire after 24h")
		}
	})
}
```

- **No network in tests.** `httptest.NewServer` on loopback is fine; nothing reaches beyond the machine.
- CI runs `go test -race -shuffle=on ./...`.
- Validator coverage is golden files: run the `validate` command over `fixtures/homelab-repo/` and compare
  with `testdata/*.golden`. Regenerate with `go test ./cmd/orchestrator -update` — pass `-update` to the
  specific packages that use the helper; `go test ./... -update` fails in packages that do not define the
  flag.

```go
// Package golden compares test output with files under testdata/.
package golden

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// Assert compares got with testdata/<name>.golden, or rewrites the file when -update is set.
func Assert(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", path, diff)
	}
}
```

---

## 8. Filesystem safety

Every write under `$APPCONFIG_STORAGE`, `$DATABASE_STORAGE` or an app directory goes through an
`os.Root`. So does every read of homelab-repo content, because the repo is untrusted input.

```go
root, err := os.OpenRoot(cfg.AppConfigStorage)
if err != nil {
	return fmt.Errorf("open appconfig root: %w", err)
}
defer root.Close()

// "${APPCONFIG_STORAGE}/authelia/secrets" is resolved to the relative name "authelia/secrets".
if err := root.MkdirAll("authelia/secrets", 0o700); err != nil {
	return fmt.Errorf("create secrets dir: %w", err) // includes escapes via .. or symlink: PATH003
}
if err := root.WriteFile("authelia/secrets/JWT_SECRET", []byte(jwt.Reveal()), 0o600); err != nil {
	return fmt.Errorf("write JWT_SECRET: %w", err)
}
```

**Why `filepath.Clean` plus a prefix check is not enough:**

- A symlink inside the root (`authelia/secrets → /etc`) passes the string check and the write lands
  outside. `os.Root` resolves each component itself and refuses links that leave the root.
- The check and the open are two steps; the path can be swapped for a symlink in between. `os.Root`
  opens relative to a held directory handle, so there is no gap.
- `strings.HasPrefix(p, "/mnt/fast/appdata")` also accepts `/mnt/fast/appdata-other`.

Rules:

- File modes are always explicit octal literals: `0o600` secrets, `0o640` config, `0o750` dirs unless
  `setup.toml` says otherwise. The umask may narrow a mode, so when `setup.toml` declares one, `Chmod`
  after creating.
- Check the error from `Close` on every file opened for writing; that is where write errors surface.
  Lint does not catch a missing check (the `std-error-handling` exclusion covers every `Close`), so this
  is enforced in review (section 10, item 6).
- **Never rewrite `compose.yaml` (D14).** `internal/compose` renders `compose.override.yaml` as bytes; the
  writer only ever writes that name. A test hashes `compose.yaml` before and after every customization
  operation and asserts it is byte-identical.

---

## 9. Tooling

`go.mod` pins the language version and the code-generation tools:

```
module github.com/cg-homelab/truenas-orchestrator

go 1.27

ignore ./frontend/node_modules

tool (
	github.com/pressly/goose/v3/cmd/goose
	github.com/sqlc-dev/sqlc/cmd/sqlc
)
```

The `ignore` directive (Go 1.25) keeps `./...` out of `frontend/node_modules`: Go does not skip
`node_modules` on its own, and npm packages such as `flatted` ship `.go` files that would otherwise be
built, vetted and linted as part of this module.

Run the tools as `go tool sqlc generate` and `go tool goose`. `internal/sqlite` carries
`//go:generate go tool sqlc generate`, so `go generate ./...` is the one regeneration command.
golangci-lint v2 and `govulncheck` are installed as pinned binaries by `just setup`, not as tool
dependencies, so their dependency graphs stay out of `go.mod`.

Language and library features the code may rely on: `log/slog`, `ServeMux` method and wildcard patterns,
range-over-func iterators, `os.Root`, the `tool` directive, `t.Context()`, generic type aliases,
`testing/synctest`, `sync.WaitGroup.Go`.

`.golangci.yml`, complete. Any finding fails `just check`; `//nolint` needs the linter name and a reason
(`//nolint:gosec // path is confined by os.Root`).

```yaml
version: "2"

linters:
  default: none
  enable:
    - errcheck
    - govet
    - staticcheck
    - revive
    - gocritic
    - errorlint
    - exhaustive
    - sloglint
    - unparam
    - gosec
    - noctx
    - bodyclose
    - misspell
    - copyloopvar
    - depguard
  settings:
    errcheck:
      check-type-assertions: true
    govet:
      enable-all: true
      disable:
        - fieldalignment # struct layout for bytes; readability wins at this scale
        - shadow # flags every nested `err :=`
    staticcheck:
      checks: ["all"]
    gocritic:
      enabled-tags: [diagnostic, style]
    exhaustive:
      default-signifies-exhaustive: false
    sloglint:
      no-global: all
      context: scope
      static-msg: true
      no-mixed-args: true
      key-naming-case: snake
      forbidden-keys: [time, level, msg, source, password, token, secret, api_key]
    depguard:
      rules:
        domain-no-io:
          files:
            - "**/internal/homelab/**"
            - "**/internal/compose/**"
            - "!$test"
          deny:
            - pkg: os
              desc: the domain is I/O-free; take bytes or values as arguments
            - pkg: io/fs
              desc: the domain is I/O-free; the caller reads files
            - pkg: net
              desc: the domain is I/O-free
            - pkg: net/http
              desc: HTTP belongs in internal/server
            - pkg: database/sql
              desc: SQL belongs in internal/sqlite
            - pkg: os/exec
              desc: running git belongs in internal/gitrepo
  exclusions:
    generated: lax
    presets:
      - std-error-handling
    rules:
      - path: _test\.go
        linters: [gosec, unparam]
      - path: internal/testutil/
        linters: [gosec]

formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    gofumpt:
      extra:
        group-params: true
        clothe-returns: true
    goimports:
      local-prefixes:
        - github.com/cg-homelab/truenas-orchestrator

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
```

Notes on the config:

- depguard matches by prefix, so `os` also denies `os/exec` and `os/signal`, and `net` denies `net/url`.
  The redundant entries document intent.
- `"!$test"` exempts test files from `domain-no-io` so they can read `testdata/`. This is the one place
  the rule bends, and only for tests.
- `std-error-handling` excludes every unchecked `Close`, `Flush` and `os.Remove`, read or write path.
  Checking `Close` on files opened for writing is therefore review-enforced (section 8 and checklist
  item 6), not lint-enforced.
- `golangci-lint config verify` validates this file; `golangci-lint fmt` applies the formatters. Pin
  golangci-lint v2.15 or later (the `gofumpt.extra` keys), in a release built with Go 1.27 or newer,
  since it refuses to lint a module whose `go` line is newer than its own toolchain.

`justfile` recipes. `check` is read-only: it never writes to the tree. `generate` is the only recipe that
writes generated code.

| Recipe | Does |
|---|---|
| `setup` | Install pinned golangci-lint, govulncheck, gofumpt (editor use) and frontend dependencies |
| `check` | `golangci-lint run`, `golangci-lint fmt --diff`, `go tool sqlc diff`, `check-api`, `govulncheck ./...`, `svelte-check` |
| `test` | `go test -race -shuffle=on ./...` plus frontend tests |
| `dev` | `go run ./cmd/orchestrator serve` against the fixtures and fake TrueNAS, plus `vite dev` |
| `build` | Build the SPA, then the binary for the host |
| `build-linux` | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath` into `dist/` |
| `build-image` | `build-linux`, then the `alpine` + git image with the binary copied in |
| `check-api` | Dump the OpenAPI document, generate the TS types into a temp directory, `diff` against `frontend/src/lib/api.d.ts` |
| `generate` | `go generate ./...` (sqlc) and `frontend/src/lib/api.d.ts`; the only recipe that writes generated files |
| `validate <path>` | `go run ./cmd/orchestrator validate <path>` |
| `smoke <host>` | Read-only calls against a real TrueNAS; never in CI |

CI runs `just check && just test && just build-linux && just build-image`.

---

## 10. Code review checklist

1. `internal/homelab` and `internal/compose` gained no I/O import, and no package imports against the arrow.
2. Every error is handled or returned with context and `%w`; callers use `errors.Is`/`As`.
3. Every blocking call takes `ctx` first; every new goroutine has an owner that stops it.
4. No secret value can reach a log, an error string, an API response or the audit log; `Reveal()` calls are justified.
5. New interfaces have a fake or a second implementation; everything else is concrete.
6. Writes under a storage root go through `os.Root` with an explicit mode and a checked `Close`; `compose.yaml` is untouched.
7. SQL changes are a new goose migration plus sqlc queries, with generated code committed.
8. Route changes are huma operations in `routes.go` and `just check-api` is green.
9. New behaviour has a table test or a golden file; new finding codes are added to `validator-checks.md` and never renumbered.
10. Destructive operations have a dry-run, an explicit confirm and an audit entry (D9).
