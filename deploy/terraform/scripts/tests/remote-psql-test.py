#!/usr/bin/env python3
"""Exercise remote_psql through a local SSH transport and external client spies."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

LIBRARY = Path(__file__).resolve().parents[1] / "lib/staging-env.sh"


class RemotePsql(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="remote-psql-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        self.host = self.work / "host"
        (self.host / "database").mkdir(parents=True)
        self.env = dict(os.environ, FIXTURE=str(self.work), AGENT_ID="agent.example",
                        CONTRIBUTOR_ID="publisher.example", BROKER_RELAY_KID="broker.test")
        self.env["PATH"] = str(self.bin) + os.pathsep + os.environ["PATH"]
        self.executable("sudo", '#!/usr/bin/env bash\nexec "$@"\n')
        self.executable("ssh", "#!" + sys.executable + '''
import os,pathlib,subprocess,sys
root=pathlib.Path(os.environ["FIXTURE"])
command=sys.argv[-1].replace("/usr/local/bin/fora-db",str(root/"bin/fora-db")).replace("/opt/fora",str(root/"host"))
raise SystemExit(subprocess.call(["bash","-c",command]))
''')
        spy = "#!" + sys.executable + '''
import json,os,pathlib,sys
root=pathlib.Path(os.environ["FIXTURE"])
(root/"call.json").write_text(json.dumps([pathlib.Path(sys.argv[0]).name,sys.argv[1:],sys.stdin.read()]))
print("result from database")
raise SystemExit(int(os.environ.get("CLIENT_STATUS","0")))
'''
        self.executable("docker", spy)
        self.executable("fora-db", spy)

    def executable(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def run_psql(self, database="fora", *args):
        command = '. ' + shlex.quote(str(LIBRARY)) + '\nSSH_CMD=(ssh fixture)\nremote_psql "$@"\n'
        return subprocess.run(["bash", "-c", command, "test", database, *args],
                              env=self.env, input="SELECT :'label';\n", text=True,
                              capture_output=True, timeout=10)

    def test_production_preserves_arguments_stdin_and_output(self):
        (self.host / "database/connection.json").write_text("{}")
        hostile = "label=O'Reilly; $(touch " + str(self.work / "injected") + ") `false` $HOME \\\nnext"
        args = ("-v", hostile, "-At", "-c", "SELECT :'label';")
        result = self.run_psql("identity", *args)
        self.assertEqual(result.returncode, 0, result.stderr)
        name, received, stdin = json.loads((self.work / "call.json").read_text())
        self.assertEqual(name, "fora-db")
        self.assertEqual(received, ["identity", *args])
        self.assertEqual(stdin, "SELECT :'label';\n")
        self.assertEqual(result.stdout, "result from database\n")
        self.assertFalse((self.work / "injected").exists())

    def test_production_missing_helper_fails_without_local_fallback(self):
        (self.host / "database/connection.json").write_text("{}")
        (self.bin / "fora-db").unlink()
        result = self.run_psql()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.work / "call.json").exists(), result.stderr)

    def test_local_stack_selects_existing_compose_user_and_database(self):
        for database, user, service in (("fora", "fora", "postgres"), ("identity", "fora", "postgres"),
                                       ("sor", "fora", "postgres"), ("console", "fora", "postgres"),
                                       ("zitadel", "zitadel", "zitadel-db")):
            with self.subTest(database=database):
                result = self.run_psql(database, "-v", "label=two words")
                self.assertEqual(result.returncode, 0, result.stderr)
                name, args, stdin = json.loads((self.work / "call.json").read_text())
                self.assertEqual(name, "docker")
                self.assertIn(service, args)
                self.assertEqual(args[args.index("-U") + 1], user)
                self.assertEqual(args[args.index("-d") + 1], database)
                self.assertIn("label=two words", args)
                self.assertIn("-T", args)
                self.assertEqual(stdin, "SELECT :'label';\n")

    def test_unknown_database_is_refused_without_ssh(self):
        result = self.run_psql("postgres")
        self.assertEqual(result.returncode, 2)
        self.assertIn("unsupported database: postgres", result.stderr)
        self.assertFalse((self.work / "call.json").exists())

    def test_installed_console_issuer_reads_the_installed_compose_file(self):
        # The ssh stub runs the remote command locally, so the installed
        # compose file's rendering comes from a docker stub answering
        # "compose config". The value is picked out on the VM: of a rendering
        # that holds secrets, only the issuer comes back over SSH.
        cases: tuple[tuple[dict | None, int, str], ...] = (
            ({"services": {"console": {"environment": {"CONSOLE_OIDC_ISSUER": "https://login.example"}},
                           "zitadel": {"command": "start --masterkey not-for-the-operator"}}},
             0, "https://login.example\n"),
            ({"services": {"exchange": {"environment": {}}}}, 0, ""),
            (None, 1, ""),
        )
        for rendered, status, out in cases:
            with self.subTest(rendered=rendered):
                self.executable("docker", "#!" + sys.executable + "\nimport json,os,sys\n"
                                + ("sys.exit(1)\n" if rendered is None else
                                   "assert sys.argv[1:] == ['compose', '-f', os.environ['FIXTURE'] + "
                                   "'/host/docker-compose.yml', 'config', '--format', 'json'], sys.argv\n"
                                   f"print(json.dumps({rendered!r}))\n"))
                command = '. ' + shlex.quote(str(LIBRARY)) + '\nSSH_CMD=(ssh fixture)\ninstalled_console_issuer\n'
                result = subprocess.run(["bash", "-c", command], env=self.env, text=True,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(result.stdout, out)

    def test_client_failure_propagates(self):
        for production in (False, True):
            with self.subTest(production=production):
                if production:
                    (self.host / "database/connection.json").write_text("{}")
                self.env["CLIENT_STATUS"] = "7"
                self.assertEqual(self.run_psql().returncode, 7)


if __name__ == "__main__":
    unittest.main()
