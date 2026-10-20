//go:build integration && zitadel

package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/tcstart"
)

// dumpZitadelLogs best-effort streams a failed container's logs to stderr.
// Diagnostic wiring: a Zitadel that fails its port-wait is otherwise opaque in
// CI, and its logs are readable over the Docker API even when the mapped port
// never became reachable (as under GitLab dind) — so the trace shows whether
// Zitadel actually booted and listened, or crashed on startup.
func dumpZitadelLogs(ctx context.Context, c testcontainers.Container) {
	if c == nil {
		return
	}
	rc, err := c.Logs(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zitadel: could not read container logs: %v\n", err)
		return
	}
	defer func() { _ = rc.Close() }()
	fmt.Fprintln(os.Stderr, "=== zitadel container logs (startup failure) ===")
	_, _ = io.Copy(os.Stderr, rc)
	fmt.Fprintln(os.Stderr, "\n=== end zitadel container logs ===")
}

const (
	zitadelImage     = "ghcr.io/zitadel/zitadel:v3.4.9"
	zitadelPGImage   = "postgres:17-alpine"
	zitadelMasterkey = "MasterkeyNeedsToHave32Characters"
	// zitadelFrontImage is the TCP forwarder the test process reaches Zitadel
	// through; see startZitadelFront for why Zitadel's own published port is not
	// used.
	zitadelFrontImage = "alpine/socat:1.8.0.3"
	// zitadelPort is the port Zitadel and its forwarder listen on inside the
	// private network. Never a host port: the host side is whatever free port
	// Docker maps the forwarder to.
	zitadelPort = "8080"
	// zitadelNetworkAlias is Zitadel's name on the private network, which the
	// forwarder dials.
	zitadelNetworkAlias = "zitadel"
	// zitadelPATPath is under /app (where the binary lives), NOT /tmp: the minimal
	// Zitadel image has no /tmp, so init-writing the PAT there fails with "no such
	// file or directory". /app exists, is root-writable, and — unlike a tmpfs mount
	// — is readable by CopyFileFromContainer (docker cp cannot read tmpfs).
	zitadelPATPath = "/app/zadmin.pat"
)

// zitadelInitSteps is the FirstInstance provisioning Zitadel runs under
// `start-from-init`: it creates the instance, the `fora` org, an admin, and the
// `fora-bootstrap` machine user whose PAT (written to zitadelPATPath) the Go
// bootstrap then authenticates with. Mirrors the retired deploy/zitadel/init-steps.yaml,
// but writes the PAT under /app (see zitadelPATPath) rather than a mounted
// /bootstrap volume the test harness does not create.
const zitadelInitSteps = `FirstInstance:
  InstanceName: "fora-poc"
  MachineKeyPath: /app/zadmin.key
  PatPath: /app/zadmin.pat
  Org:
    Name: "fora"
    Human:
      UserName: "zadmin"
      FirstName: "Z"
      LastName: "Admin"
      Email:
        Address: "zadmin@fora.localhost"
        Verified: true
      Password: "ZAdmin123!"
      PasswordChangeRequired: false
    Machine:
      Machine:
        Username: "fora-bootstrap"
        Name: "FORA bootstrap service account"
      MachineKey:
        ExpirationDate: "2099-01-01T00:00:00Z"
        Type: 1
      Pat:
        ExpirationDate: "2099-01-01T00:00:00Z"
`

// SharedZitadel is one real Zitadel v3 instance (plus its own Postgres) brought up
// once per package for the `zitadel` test tier. It carries the coordinates the
// oidcup client and the headless-login helper need. There is no per-test reset:
// each test resets the identity service's own Postgres + Vault and drives a fresh
// login, so the Zitadel instance is read-mostly shared config, not per-test state.
type SharedZitadel struct {
	Issuer       string
	ClientID     string
	ClientSecret string
}

// StartSharedZitadel brings up Zitadel v3.4.9 (pinned — v4.x has an open
// migration-03 boot crash) plus its dedicated Postgres on a private network,
// waits for first-instance init to write the admin PAT, then bootstraps the
// `fora-mcp` OIDC client and the `alice@acme.local` login user. Returns a handle
// plus a cleanup func for the caller (TestMain) to defer. Sibling of
// StartSharedVault, but heavier: expect ~30-45s startup. Nothing it starts binds
// a fixed host port, so any number of these run side by side on one daemon.
func StartSharedZitadel(ctx context.Context, logger *slog.Logger) (*SharedZitadel, func(), error) {
	net, err := network.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create zitadel network: %w", err)
	}
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	cleanups = append(cleanups, func() {
		if rmErr := net.Remove(context.Background()); rmErr != nil {
			logger.Warn("remove zitadel network", "err", rmErr)
		}
	})

	pgC, err := startZitadelPostgres(ctx, net.Name)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cleanups = append(cleanups, terminateFn(logger, "zitadel postgres", pgC))

	frontC, host, port, err := startZitadelFront(ctx, net.Name)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cleanups = append(cleanups, terminateFn(logger, "zitadel front", frontC))

	ziC, err := startZitadelContainer(ctx, net.Name, host, port)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cleanups = append(cleanups, terminateFn(logger, "zitadel", ziC))

	issuer := "http://" + host + ":" + port
	if err := waitZitadelReady(ctx, issuer); err != nil {
		dumpZitadelLogs(ctx, ziC)
		cleanup()
		return nil, nil, err
	}
	pat, err := waitForPAT(ctx, ziC)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	creds, err := bootstrapZitadel(ctx, issuer, host, pat)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &SharedZitadel{Issuer: issuer, ClientID: creds.clientID, ClientSecret: creds.clientSecret}, cleanup, nil
}

func startZitadelPostgres(ctx context.Context, netName string) (testcontainers.Container, error) {
	c, err := tcstart.Run(ctx, func(ctx context.Context) (testcontainers.Container, error) {
		return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: zitadelPGImage,
				Env: map[string]string{
					"POSTGRES_USER":     "zitadel",
					"POSTGRES_PASSWORD": "zitadel",
					"POSTGRES_DB":       "zitadel",
				},
				ExposedPorts:   []string{"5432/tcp"},
				Networks:       []string{netName},
				NetworkAliases: map[string][]string{netName: {"zitadel-db"}},
				WaitingFor:     wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("start zitadel postgres: %w", err)
	}
	return c, nil
}

// startZitadelFront starts the forwarder that publishes Zitadel to the test
// process, and returns it with the host and port the test process reaches it at.
//
// Zitadel derives its issuer, JWKS URL and authorize/token endpoints from
// ExternalDomain:ExternalPort, baked at init, not from the request Host, so
// go-oidc discovery only succeeds when the client reaches Zitadel at exactly that
// address. The address must therefore be known before Zitadel starts, and
// Docker names an ephemeral host port only once the container publishing it has
// started. The forwarder breaks the cycle: it starts first, on an ephemeral host
// port, and Zitadel starts after it, with that host and port as its external
// address. testcontainers also publishes the port the Zitadel image declares, on
// a second ephemeral host port, but nothing uses that one: the test process
// reaches Zitadel only through the forwarder. Neither container holds a fixed
// host port, so two runs on one daemon never contend for one.
//
// The host is the one the TEST PROCESS reaches published ports at — "localhost"
// against a local daemon, but "docker" under GitLab dind, where the daemon runs
// in the `docker:dind` service and the job container reaches it by that
// hostname. It becomes ExternalDomain, the issuer host and the bootstrap Host
// header. The binding is on every interface, not loopback only, because under
// dind the job container reaches it from outside the daemon's host.
func startZitadelFront(ctx context.Context, netName string) (c testcontainers.Container, host, port string, err error) {
	c, err = tcstart.Run(ctx, func(ctx context.Context) (testcontainers.Container, error) {
		return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: zitadelFrontImage,
				Cmd: []string{
					"TCP-LISTEN:" + zitadelPort + ",fork,reuseaddr",
					"TCP:" + zitadelNetworkAlias + ":" + zitadelPort,
				},
				ExposedPorts: []string{zitadelPort + "/tcp"},
				Networks:     []string{netName},
				WaitingFor:   wait.ForListeningPort(zitadelPort + "/tcp").WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		})
	})
	if err != nil {
		return c, "", "", fmt.Errorf("start zitadel front: %w", err)
	}
	host, err = c.Host(ctx)
	if err != nil {
		return c, "", "", fmt.Errorf("resolve docker daemon host: %w", err)
	}
	mapped, err := c.MappedPort(ctx, zitadelPort+"/tcp")
	if err != nil {
		return c, "", "", fmt.Errorf("zitadel front port: %w", err)
	}
	return c, host, mapped.Port(), nil
}

func startZitadelContainer(ctx context.Context, netName, host, port string) (testcontainers.Container, error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: zitadelImage,
			Cmd: []string{
				"start-from-init", "--masterkey", zitadelMasterkey,
				"--tlsMode", "disabled", "--steps", "/etc/zitadel/init-steps.yaml",
			},
			Env:            zitadelEnv(host, port),
			Networks:       []string{netName},
			NetworkAliases: map[string][]string{netName: {zitadelNetworkAlias}},
			Files: []testcontainers.ContainerFile{{
				Reader:            strings.NewReader(zitadelInitSteps),
				ContainerFilePath: "/etc/zitadel/init-steps.yaml",
				FileMode:          0o644,
			}},
			ConfigModifier: func(c *container.Config) { c.User = "0" },
			// No wait strategy: the test process reaches Zitadel only through
			// the forwarder, so readiness is polled there (waitZitadelReady).
			// The port testcontainers publishes for the image goes unused.
		},
		Started: true,
	})
	if err != nil {
		dumpZitadelLogs(ctx, c)
		return nil, fmt.Errorf("start zitadel: %w", err)
	}
	return c, nil
}

// zitadelReadyTimeout bounds Zitadel's start-from-init, which runs its
// migrations and first-instance setup before it serves; under GitLab dind that
// has taken well over a minute.
const zitadelReadyTimeout = 180 * time.Second

// waitZitadelReady polls Zitadel's readiness endpoint through the forwarder
// until it answers 200. A refused or reset connection is the forwarder finding
// nothing listening yet, so it is retried like any other not-ready answer.
func waitZitadelReady(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, zitadelReadyTimeout)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/debug/ready", http.NoBody)
		if err != nil {
			return fmt.Errorf("zitadel readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("zitadel never became ready at %s: %w", base, errors.Join(last, ctx.Err()))
		case <-time.After(time.Second):
		}
	}
}

// zitadelEnv mirrors the retired docker-compose.e2e.yml zitadel service: a
// non-TLS instance listening on 8080 inside the private network, backed by the
// `zitadel-db` Postgres reached over the same network. host and port are where
// the test process reaches it through the forwarder (localhost locally, docker
// under dind, on an ephemeral port); they become ExternalDomain and
// ExternalPort, so Zitadel's issuer and endpoint URLs are the ones the client
// can actually reach.
func zitadelEnv(host, port string) map[string]string {
	return map[string]string{
		"ZITADEL_EXTERNALDOMAIN":                   host,
		"ZITADEL_EXTERNALPORT":                     port,
		"ZITADEL_EXTERNALSECURE":                   "false",
		"ZITADEL_PORT":                             zitadelPort,
		"ZITADEL_TLS_ENABLED":                      "false",
		"ZITADEL_DATABASE_POSTGRES_HOST":           "zitadel-db",
		"ZITADEL_DATABASE_POSTGRES_PORT":           "5432",
		"ZITADEL_DATABASE_POSTGRES_DATABASE":       "zitadel",
		"ZITADEL_DATABASE_POSTGRES_USER_USERNAME":  "zitadel",
		"ZITADEL_DATABASE_POSTGRES_USER_PASSWORD":  "zitadel",
		"ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE":  "disable",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME": "zitadel",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD": "zitadel",
		"ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE": "disable",
	}
}

// waitForPAT polls the admin PAT out of the container. Zitadel may report its
// port listening a beat before FirstInstance finishes writing the PAT, so this
// retries the copy until the file is non-empty rather than reading once.
func waitForPAT(ctx context.Context, c testcontainers.Container) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		if pat := tryReadPAT(ctx, c); pat != "" {
			return pat, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("zitadel: admin PAT %s not written: %w", zitadelPATPath, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func tryReadPAT(ctx context.Context, c testcontainers.Container) string {
	r, err := c.CopyFileFromContainer(ctx, zitadelPATPath)
	if err != nil {
		return ""
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func terminateFn(logger *slog.Logger, name string, c testcontainers.Container) func() {
	return func() {
		if err := c.Terminate(context.Background()); err != nil {
			logger.Warn("terminate "+name, "err", err)
		}
	}
}
