"""PostgreSQL for the tests of the operator scripts that run SQL on the VM.

The operator scripts run their SQL on the VM through remote_psql: over SSH, as
sudo /usr/local/bin/fora-db. Their tests run a script over stubbed Terraform
and SSH against a real PostgreSQL 16 in a container, and the SSH stub sends
each fora-db call to psql in that container, with the arguments the script
passed. This module holds what those tests share: the container, psql, the
migrated template databases a test resets from, the stub writer, the
environment a script runs in, and the fora-db relay.

Every docker call is bounded, so a stuck container fails the test instead of
holding the suite until CI's job limit.
"""
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

IMAGE = "postgres:16-alpine"
TIMEOUT = 120


def docker(*args, stdin=None, check=True):
    return subprocess.run(["docker", *args], input=stdin, text=True, capture_output=True,
                          check=check, timeout=TIMEOUT)


def start(prefix, cleanup):
    """Starts a PostgreSQL container, registers its removal with cleanup (a
    unittest addClassCleanup), and returns its name once it accepts
    connections."""
    container = prefix + "-" + uuid.uuid4().hex[:12]
    docker("run", "-d", "--name", container, "-e", "POSTGRES_HOST_AUTH_METHOD=trust", IMAGE)
    cleanup(docker, "rm", "-f", container, check=False)
    # Over TCP: the image's init phase runs a temporary server that listens on
    # the socket only, and pg_isready on the socket passes before the real
    # server is up.
    for _ in range(60):
        if docker("exec", container, "pg_isready", "-h", "localhost", "-U", "postgres",
                  check=False).returncode == 0:
            return container
        time.sleep(1)
    raise RuntimeError("test PostgreSQL did not start")


def psql(container, database, sql):
    """Runs sql as postgres in database, stopping at the first error, and
    returns the unaligned, tuples-only output."""
    return docker("exec", "-i", container, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres",
                  "-d", database, stdin=sql).stdout.strip()


def migrate_template(container, database, migrations):
    """Creates <database>_base and applies every *.up.sql in migrations to it,
    in order. A test then starts from a fresh copy (reset)."""
    psql(container, "postgres", f"CREATE DATABASE {database}_base;")
    for migration in sorted(Path(migrations).glob("*.up.sql")):
        psql(container, f"{database}_base", migration.read_text())


def reset(container, database):
    """Replaces database with a fresh copy of its migrated template."""
    psql(container, "postgres", f"DROP DATABASE IF EXISTS {database} WITH (FORCE);")
    psql(container, "postgres", f"CREATE DATABASE {database} TEMPLATE {database}_base;")


def install(directory, name, body):
    """Writes the executable Python stub name into directory."""
    path = Path(directory) / name
    path.write_text("#!" + sys.executable + "\n" + body)
    path.chmod(0o755)


def operator_env(work, container, **extra):
    """The environment an operator script runs in here: the stubs in work
    first on PATH, a production-aws STACK_DIR, the test container for the
    fora-db relay, and the ids staging-env.sh would otherwise read from key
    files."""
    return dict(os.environ, PATH=str(work) + os.pathsep + os.environ["PATH"],
                STACK_DIR=str(Path(work) / "production-aws"), TEST_DB=container,
                AGENT_ID="unused", CONTRIBUTOR_ID="unused", BROKER_RELAY_KID="unused", **extra)


# The end of an SSH stub, which has `command` set to the remote command: the
# fora-db call (remote_psql's production branch) goes to psql in the TEST_DB
# container, in the database the script named, with the arguments it passed.
FORA_DB_RELAY = '''
import os, shlex, subprocess
line = next(line for line in command.splitlines() if "sudo /usr/local/bin/fora-db" in line)
_, _, database, *args = shlex.split(line)
raise SystemExit(subprocess.call(["docker", "exec", "-i", os.environ["TEST_DB"], "psql", "-X", "-U", "postgres",
                                  "-d", database, *args], timeout=%d))
''' % TIMEOUT
