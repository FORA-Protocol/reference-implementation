"""A real Zitadel for the operator tests that run deploy/zitadel/bootstrap/.

Zitadel runs the image the compose-stack module deploys, booted the way
docker-compose.e2e.yml boots it: `start-from-init` with the e2e masterkey,
plain http, and the FirstInstance steps in deploy/zitadel/init-steps.yaml, on
PostgreSQL 16. With system_api, Zitadel also trusts the System API user
`fora-instance-admin` with the key local-system-key.sh keeps, as e2e does; with
mail, it sends every instance's email to Mailpit through DefaultInstance SMTP
settings, as e2e does, with the Mailpit image e2e runs. The scripts run in the
images the stacks run them in: curl for the bootstrap and the instance step,
openssl for the System API key. start_caddy puts Caddy in front of Zitadel with
the Zitadel site the compose-stack module renders.

The expectations both real-Zitadel tests share live here too: the digest of a
branding file (mark), the email texts, the developers' run, and the checks of
an OIDC client read back from Zitadel.

Every container sits on a private network, no port is published to the host
and no host path is mounted, so the tests need nothing but the Docker API and
work with a remote daemon (DOCKER_HOST) as with a local one. The network, the
containers and the volumes share one random prefix and are removed afterwards,
also when a test fails.
"""
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import time
import urllib.parse
import uuid

REPO = Path(__file__).resolve().parents[4]
BOOTSTRAP_DIR = REPO / 'deploy/zitadel/bootstrap'
INIT_STEPS = REPO / 'deploy/zitadel/init-steps.yaml'
MODULE_COMPOSE = REPO / 'deploy/terraform/modules/compose-stack/templates/docker-compose.yml.tftpl'
MODULE_CADDYFILE = REPO / 'deploy/terraform/modules/compose-stack/templates/Caddyfile.tftpl'
E2E_COMPOSE = REPO / 'docker-compose.e2e.yml'
POSTGRES_IMAGE = 'postgres:16-alpine'
CURL_IMAGE = 'curlimages/curl:8.11.1'
OPENSSL_IMAGE = 'alpine/openssl:3.3.2'
# The masterkey docker-compose.e2e.yml starts its throwaway Zitadel with.
MASTERKEY = 'MasterkeyNeedsToHave32Characters'
# Zitadel routes instances by the Host header. The developer instance answers
# at its ExternalDomain; the publisher instance at the address it is created
# with, which the Zitadel container also carries as a network alias.
DEVELOPERS = 'zitadel'
PUBLISHERS = 'zitadel-publishers'
ZITADEL = 'http://zitadel:8080'
MAILPIT = 'http://mailpit:8025'
CADDY = 'http://caddy:80'
# The System API user as e2e trusts it: no memberships, so SYSTEM_OWNER.
SYSTEM_API_USERS = '{"fora-instance-admin":{"Path":"/systemapi/public.pem"}}'
# The four account emails customized by bootstrap.
EMAIL_TYPES = ('init', 'verifyemail', 'passwordreset', 'password_change')


def mark(name):
    """The sha256 of a branding file, as look() reports an asset."""
    return hashlib.sha256((BOOTSTRAP_DIR / 'branding' / name).read_bytes()).hexdigest()


def emails(side):
    """Each email's title and subject on one side."""
    return {kind: {'title': text, 'subject': text} for kind, text in (
        ('init', f'Welcome to {side}'), ('verifyemail', f'{side}: Verify your email'),
        ('passwordreset', f'{side}: Reset your password'),
        ('password_change', f'{side}: Your password has changed'))}


def developers_env(identity, **extra):
    """The environment of a developers' bootstrap run against the testbed's Zitadel."""
    return {'ZITADEL_INSTANCE_ROLE': 'developers', 'ZITADEL_BASE_URL': ZITADEL, 'ZITADEL_INSTANCE_HOST': DEVELOPERS,
            'ZITADEL_ISSUER': ZITADEL, 'IDENTITY_AUTH_ISSUER': identity, **extra}


def assert_app(case, config, redirects, logouts, dev_mode):
    """An OIDC client's settings as Zitadel stored them."""
    case.assertEqual(config.get('redirectUris', []), redirects, config)
    case.assertEqual(config.get('postLogoutRedirectUris', []), logouts, config)
    # proto3 JSON leaves out a field at its zero value: false, an empty list,
    # and an enum's first value, which is WEB and BASIC for these two. Any
    # other type or method would be present.
    case.assertIs(config.get('devMode', False), dev_mode, config)
    case.assertEqual(config.get('appType', 'OIDC_APP_TYPE_WEB'), 'OIDC_APP_TYPE_WEB', config)
    case.assertEqual(config.get('authMethodType', 'OIDC_AUTH_METHOD_TYPE_BASIC'), 'OIDC_AUTH_METHOD_TYPE_BASIC', config)
    case.assertIs(config.get('idTokenUserinfoAssertion'), True, config)


def assert_sign_in(case, bed, host, config, redirects, refused=()):
    """The instance's authorize endpoint starts a sign-in for each redirect
    address in redirects, and refuses each one in refused."""
    for redirect in redirects:
        status, location = bed.authorize(host, config['clientId'], redirect)
        case.assertEqual(status, '302', redirect)
        case.assertIn('/ui/login/login?authRequestID=', location)
    for redirect in refused:
        case.assertEqual(bed.authorize(host, config['clientId'], redirect)[0], '400', redirect)


def zitadel_image():
    """The Zitadel image the compose-stack module deploys."""
    images = re.findall(r'^\s*image:\s*(ghcr\.io/zitadel/zitadel:\S+)\s*$', MODULE_COMPOSE.read_text(), re.M)
    assert len(images) == 1, f'expected one Zitadel image in {MODULE_COMPOSE}, found {images}'
    return images[0]


def mailpit_image():
    """The Mailpit image the e2e stack catches its mail with."""
    images = re.findall(r'^\s*image:\s*(axllent/mailpit:\S+)\s*$', E2E_COMPOSE.read_text(), re.M)
    assert len(images) == 1, f'expected one Mailpit image in {E2E_COMPOSE}, found {images}'
    return images[0]


def caddy_image():
    """The Caddy image the compose-stack module deploys."""
    images = re.findall(r'^\s*image:\s*(caddy:\S+)\s*$', MODULE_COMPOSE.read_text(), re.M)
    assert len(images) == 1, f'expected one Caddy image in {MODULE_COMPOSE}, found {images}'
    return images[0]


def zitadel_site():
    """The inside of the Caddy site that serves both Zitadel instances, as the
    module renders it while a System API user exists: the template's lines
    between the site's address line and its closing brace, without the
    template's own %{ } directives."""
    lines = MODULE_CADDYFILE.read_text().splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith('${zitadel_hostname}'))
    end = next(i for i in range(start + 1, len(lines)) if lines[i] == '}')
    body = [line for line in lines[start + 1:end] if not line.lstrip().startswith('%{')]
    rendered = '\n'.join(body).replace('${console_login_hostname}', PUBLISHERS)
    rendered = rendered.replace('${zitadel_hostname}', DEVELOPERS)
    rendered = rendered.replace('${developer_login_css}', (REPO / 'deploy/zitadel/developer-login.css').read_text())
    rendered = rendered.replace('${publisher_login_css}', (REPO / 'deploy/zitadel/publisher-login.css').read_text())
    assert '${' not in rendered, 'the Zitadel site now renders a value: render it here too'
    return rendered


def docker(*args, input=None, timeout=120, check=True):
    """Run one docker command; text output unless input is bytes."""
    return subprocess.run(['docker', *args], input=input, capture_output=True, timeout=timeout,
                          check=check, text=not isinstance(input, bytes))


def env_options(environment):
    return [part for key, value in environment.items() for part in ('-e', key + '=' + value)]


def file_archive(path, data):
    """One file as a tar stream for `docker cp -`, at path (relative to /)."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w') as archive:
        directory = tarfile.TarInfo(path.rpartition('/')[0])
        directory.type, directory.mode = tarfile.DIRTYPE, 0o755
        archive.addfile(directory)
        member = tarfile.TarInfo(path)
        member.size, member.mode = len(data), 0o644
        archive.addfile(member, io.BytesIO(data))
    return buffer.getvalue()


class Testbed:
    """One Zitadel and its neighbours. cleanup registers a teardown step (a test
    class's addClassCleanup); the steps run in reverse, so the network goes last."""

    # Where each volume is mounted, in Zitadel, in the script runs and,
    # read-only, in the client container.
    VOLUMES = {
        'shared': '/bootstrap',          # FirstInstance's admin token; the identity client's files
        'systemapi': '/systemapi',       # the System API key pair and a fresh token
        'publishers': '/publishers',     # the publisher instance's bootstrap token
        'console': '/console-oidc',      # the console client's files
    }

    def __init__(self, cleanup, *, system_api=False, mail=False):
        self.name = 'zitadel-test-' + uuid.uuid4().hex[:10]
        self.network = self.name + '-net'
        self.volumes = {key: f'{self.name}-{key}' for key in self.VOLUMES}
        self.db, self.zitadel, self.client = self.name + '-db', self.name + '-zitadel', self.name + '-client'
        self.mailpit, self.caddy = self.name + '-mailpit', self.name + '-caddy'
        self.system_api, self.mail = system_api, mail
        self.runs = 0
        cleanup(docker, 'network', 'rm', self.network, check=False)
        for volume in self.volumes.values():
            cleanup(docker, 'volume', 'rm', '-f', volume, check=False)
        for container in (self.db, self.zitadel, self.client, self.mailpit, self.caddy):
            cleanup(docker, 'rm', '-f', '-v', container, check=False)
        self.cleanup = cleanup

    def mounts(self, read_only=False, only=None):
        return [part for key, volume in self.volumes.items() if only is None or key in only
                for part in ('-v', f'{volume}:{self.VOLUMES[key]}{":ro" if read_only else ""}')]

    def start(self):
        docker('network', 'create', self.network)
        for volume in self.volumes.values():
            docker('volume', 'create', volume)
        docker('run', '-d', '--name', self.db, '--network', self.network, '--network-alias', 'zitadel-db',
               '-e', 'POSTGRES_USER=zitadel', '-e', 'POSTGRES_PASSWORD=zitadel', '-e', 'POSTGRES_DB=zitadel',
               POSTGRES_IMAGE, timeout=300)
        if self.mail:
            docker('run', '-d', '--name', self.mailpit, '--network', self.network, '--network-alias', 'mailpit',
                   mailpit_image(), timeout=300)
        self.wait_for('PostgreSQL', lambda: docker('exec', self.db, 'pg_isready', '-h', '127.0.0.1', '-U', 'zitadel',
                                                    '-d', 'zitadel', check=False).returncode == 0, self.db)
        self.create_zitadel(SYSTEM_API_USERS if self.system_api else None)
        # A long-lived curl container on the network reads the volumes and
        # calls Zitadel for the checks; exec is far cheaper than a run each.
        # root, because the scripts write their tokens readable by root only.
        docker('run', '-d', '--name', self.client, '--network', self.network, '--user', '0', '--entrypoint', 'sleep',
               *self.mounts(read_only=True), CURL_IMAGE, '3600', timeout=300)
        self.wait_ready()

    def create_zitadel(self, system_api_users):
        """Creates and starts Zitadel; system_api_users is ZITADEL_SYSTEMAPIUSERS
        (JSON), or None for no System API user."""
        environment = {
            'ZITADEL_EXTERNALDOMAIN': DEVELOPERS, 'ZITADEL_EXTERNALPORT': '8080',
            'ZITADEL_EXTERNALSECURE': 'false', 'ZITADEL_PORT': '8080', 'ZITADEL_TLS_ENABLED': 'false',
            'ZITADEL_DATABASE_POSTGRES_HOST': 'zitadel-db', 'ZITADEL_DATABASE_POSTGRES_PORT': '5432',
            'ZITADEL_DATABASE_POSTGRES_DATABASE': 'zitadel',
            'ZITADEL_DATABASE_POSTGRES_USER_USERNAME': 'zitadel',
            'ZITADEL_DATABASE_POSTGRES_USER_PASSWORD': 'zitadel',
            'ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE': 'disable',
            'ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME': 'zitadel',
            'ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD': 'zitadel',
            'ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE': 'disable',
        }
        if system_api_users is not None:
            # As docker-compose.e2e.yml: Zitadel reads the key when it first
            # checks a token, so the key may be made after Zitadel starts.
            environment['ZITADEL_SYSTEMAPIUSERS'] = system_api_users
        if self.mail:
            environment.update({
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_HOST': 'mailpit:1025',
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_USER': '',
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_PASSWORD': '',
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_TLS': 'false',
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROM': 'noreply@example.test',
                'ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROMNAME': 'FORA',
            })
        # root, as in e2e, so FirstInstance can write the PAT into the shared volume.
        docker('create', '--name', self.zitadel, '--network', self.network, '--network-alias', DEVELOPERS,
               '--network-alias', PUBLISHERS, '--user', '0', *env_options(environment),
               *self.mounts(only=('shared',)), *self.mounts(read_only=True, only=('systemapi',)), zitadel_image(),
               'start-from-init', '--masterkey', MASTERKEY, '--tlsMode', 'disabled',
               '--steps', '/etc/zitadel/init-steps.yaml', timeout=300)
        docker('cp', '-', self.zitadel + ':/', input=file_archive('etc/zitadel/init-steps.yaml', INIT_STEPS.read_bytes()))
        docker('start', self.zitadel)

    def restart_zitadel(self, system_api_users):
        """Zitadel again, on the same database and volumes, with another
        ZITADEL_SYSTEMAPIUSERS: how an operator opens and closes a recovery
        window, through a release that changes that setting."""
        docker('rm', '-f', self.zitadel)
        self.create_zitadel(system_api_users)
        self.wait_ready()

    def wait_ready(self):
        self.wait_for('Zitadel', lambda: self.curl('-o', '/dev/null', '-w', '%{http_code}',
                                                   ZITADEL + '/debug/ready') == '200', self.zitadel)
        # Ready is not yet serving: the management API answers once FirstInstance
        # has written the admin token and the instance's read model is built.
        self.wait_for('the management API', lambda: self.status_of(DEVELOPERS, self.developer_pat(),
                                                                    'GET', '/management/v1/orgs/me') == 200,
                      self.zitadel)

    def wait_for(self, what, ready, container, seconds=180):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if ready():
                return
            time.sleep(.5)
        logs = docker('logs', container, check=False)
        raise AssertionError(f'{what} was not ready within {seconds}s:\n{logs.stdout}{logs.stderr}')

    # ── reading ──────────────────────────────────────────────────────────────

    def curl(self, *args, host=DEVELOPERS):
        """curl from the client container, with an instance's Host header; stdout."""
        return docker('exec', self.client, 'curl', '-s', '-H', 'Host: ' + host, *args, check=False).stdout

    def read(self, path):
        """A file in one of the volumes, as the client container sees it; None when absent."""
        result = docker('exec', self.client, 'cat', path, check=False)
        return result.stdout.strip() if result.returncode == 0 else None

    def mode(self, path):
        """A file's permission bits, as octal digits."""
        return docker('exec', self.client, 'stat', '-c', '%a', path).stdout.strip()

    def developer_pat(self):
        return self.read('/bootstrap/zadmin.pat') or ''

    def publisher_pat(self):
        return self.read('/publishers/bootstrap.pat') or ''

    def call(self, host, pat, method, path, body=None):
        """One API call to an instance; (status, parsed body or None)."""
        args = ['-o', '-', '-w', '\n%{http_code}', '-X', method, '-H', 'Authorization: Bearer ' + pat]
        if body is not None:
            args += ['-H', 'Content-Type: application/json', '-d', json.dumps(body)]
        out, _, status = self.curl(*args, ZITADEL + path, host=host).rpartition('\n')
        try:
            return int(status), json.loads(out) if out.strip() else None
        except ValueError:
            return int(status or 0), None

    def status_of(self, host, pat, method, path):
        return self.call(host, pat, method, path)[0]

    def api(self, host, pat, method, path, body=None):
        """A call that must succeed; its parsed body."""
        status, parsed = self.call(host, pat, method, path, body)
        assert 200 <= status < 300, f'{method} {path} on {host}: {status} {parsed}'
        return parsed

    def asset(self, host, pat, path):
        """The bytes of one of the instance's active label-policy assets, e.g.
        logo/dark; None when the instance has none (404). Any other failure
        fails the test: it is not an absent asset."""
        result = docker('exec', self.client, 'curl', '-s', '-o', '/tmp/asset', '-w', '%{http_code}',
                        '-H', 'Host: ' + host, '-H', 'Authorization: Bearer ' + pat,
                        ZITADEL + '/assets/v1/instance/policy/label/' + path, input=b'')
        status = result.stdout.decode()
        if status == '404':
            return None
        assert status == '200', f'reading the {path} asset of {host}: {status}'
        return docker('exec', self.client, 'cat', '/tmp/asset', input=b'').stdout

    def instances_at(self, domain):
        """The names of the instances the System API finds at an address, asked
        with a fresh token that must be accepted. The System API names its
        instance in the request, so the call carries no instance's Host."""
        self.system_key()
        found = self.api('', self.read('/systemapi/token'), 'POST', '/system/v1/instances/_search',
                         {'queries': [{'domainQuery': {'domains': [domain]}}]})
        return [instance['name'] for instance in found.get('result', [])]

    def user_id(self, host, pat):
        """The id of the user a token belongs to."""
        return self.api(host, pat, 'GET', '/auth/v1/users/me')['user']['id']

    def events_by(self, host, pat, editor):
        """How many events the instance holds that the user editor wrote."""
        events = self.api(host, pat, 'POST', '/admin/v1/events/_search',
                          {'editorUserId': editor, 'limit': 1000, 'asc': True}).get('events', [])
        assert len(events) < 1000, 'the event count reached the search limit'
        return len(events)

    def apps(self, host, pat):
        """The apps of project fora, by name, as the management API returns them."""
        query = {'queries': [{'nameQuery': {'name': 'fora', 'method': 'TEXT_QUERY_METHOD_EQUALS'}}]}
        projects = self.api(host, pat, 'POST', '/management/v1/projects/_search', query).get('result', [])
        if not projects:
            return {}
        found = self.api(host, pat, 'POST', f'/management/v1/projects/{projects[0]["id"]}/apps/_search', {})
        return {app['name']: app['oidcConfig'] for app in found.get('result', [])}

    def token_error(self, host, client_id, secret):
        """The token endpoint's error for a code it never issued. A wrong secret
        fails client authentication first, with invalid_client; a right one gets
        past it, to the refusal of the code."""
        out = self.curl('-u', client_id + ':' + secret, '-d', 'grant_type=authorization_code',
                        '-d', 'code=never-issued', '-d', 'redirect_uri=https://unused.example/cb',
                        ZITADEL + '/oauth/v2/token', host=host)
        return json.loads(out)['error']

    def authorize(self, host, client_id, redirect):
        """The status and target of a sign-in request for that redirect address."""
        query = urllib.parse.urlencode({'client_id': client_id, 'redirect_uri': redirect,
                                        'response_type': 'code', 'scope': 'openid'})
        status, _, location = self.curl('-o', '/dev/null', '-w', '%{http_code} %{redirect_url}',
                                        ZITADEL + '/oauth/v2/authorize?' + query, host=host).partition(' ')
        return status, location

    def look(self, host, pat):
        """What an instance shows, read back from Zitadel: the active label
        policy's colors, the sha256 of each active asset (None when absent),
        the allowed languages, the three page titles, and each email's title
        and subject."""
        policy = self.api(host, pat, 'GET', '/admin/v1/policies/label')['policy']
        colors = {key: policy.get(key) for key in ('primaryColor', 'backgroundColor', 'warnColor', 'fontColor',
                                                    'primaryColorDark', 'backgroundColorDark', 'warnColorDark',
                                                    'fontColorDark')}
        assets = {}
        for path in ('logo', 'logo/dark', 'icon', 'icon/dark', 'font'):
            data = self.asset(host, pat, path)
            assets[path] = None if data is None else hashlib.sha256(data).hexdigest()
        texts = self.api(host, pat, 'GET', '/admin/v1/text/login/en')['customText']
        return {
            'colors': colors,
            'assets': assets,
            'languages': self.api(host, pat, 'GET', '/admin/v1/restrictions').get('allowedLanguages', []),
            'titles': [texts.get(section, {}).get('title') for section in
                       ('loginText', 'registrationOptionText', 'registrationUserText')],
            'emails': {kind: {key: self.api(host, pat, 'GET', f'/admin/v1/text/message/{kind}/en')['customText'].get(key)
                              for key in ('title', 'subject')}
                       for kind in EMAIL_TYPES},
        }

    # ── running the scripts ─────────────────────────────────────────────────

    def container(self, image, environment, *command):
        """A fresh container of image running command, with every volume mounted
        and the bootstrap directory at /zitadel; returns (status, stdout, stderr)."""
        self.runs += 1
        name = f'{self.name}-run{self.runs}'
        self.cleanup(docker, 'rm', '-f', name, check=False)
        # The image's entrypoint and non-root user both go, as bootstrap-identity.sh does it.
        docker('create', '--name', name, '--network', self.network, '--user', '0', '--entrypoint', 'sh',
               *env_options(environment), *self.mounts(), image, *command, timeout=300)
        # The whole directory, as the stacks mount it: the scripts source lib.sh.
        docker('cp', str(BOOTSTRAP_DIR), name + ':/zitadel')
        result = docker('start', '-a', name, check=False, timeout=300)
        return result.returncode, result.stdout, result.stderr

    def run(self, script, environment, image=CURL_IMAGE):
        """One script of the bootstrap directory, as the stacks run it."""
        return self.container(image, environment, '/zitadel/' + script)

    def shell(self, command, image=CURL_IMAGE):
        """One shell command that must succeed, e.g. to move a file in a volume."""
        status, _, err = self.container(image, {}, '-c', command)
        assert status == 0, err

    def system_key(self, audience=ZITADEL):
        """local-system-key.sh, as e2e's key one-shot runs it: the key pair once,
        a fresh System API token every run."""
        status, _, err = self.run('local-system-key.sh', {'KEY_DIR': '/systemapi', 'AUDIENCE': audience},
                                  image=OPENSSL_IMAGE)
        assert status == 0, err

    # ── Caddy ───────────────────────────────────────────────────────────────

    def start_caddy(self):
        """Caddy in front of Zitadel, at caddy:80, serving the Zitadel site the
        module renders. Plain http: what is tested is the site, not TLS."""
        config = '{\n\tadmin off\n\tauto_https off\n}\n\nhttp://:80 {\n' + zitadel_site() + '\n}\n'
        docker('create', '--name', self.caddy, '--network', self.network, '--network-alias', 'caddy', caddy_image(),
               timeout=300)
        docker('cp', '-', self.caddy + ':/', input=file_archive('etc/caddy/Caddyfile', config.encode()))
        docker('start', self.caddy)
        self.wait_for('Caddy', lambda: self.curl('-o', '/dev/null', '-w', '%{http_code}',
                                                 CADDY + '/debug/ready') == '200', self.caddy)

    def answer_at(self, base, host, pat, method, path, body=None):
        """(status, body) of one call to base (ZITADEL or CADDY), with a JSON
        body when one is given."""
        args = ['-o', '-', '-w', '\n%{http_code}', '-X', method, '-H', 'Authorization: Bearer ' + pat]
        if body is not None:
            args += ['-H', 'Content-Type: application/json', '-d', json.dumps(body)]
        out, _, status = self.curl(*args, base + path, host=host).rpartition('\n')
        return int(status or 0), out

    def grpc_web_answer(self, base, host, pat, path):
        """(status, body) of an empty gRPC-Web call, the protocol the Zitadel
        console speaks, to path (/<package>.<Service>/<Method>)."""
        result = docker('exec', '-i', self.client, 'curl', '-s', '-o', '-', '-w', '\n%{http_code}',
                        '-X', 'POST', '-H', 'Host: ' + host, '-H', 'Authorization: Bearer ' + pat,
                        '-H', 'Content-Type: application/grpc-web+proto', '-H', 'X-Grpc-Web: 1',
                        '--data-binary', '@-', base + path, input=b'\x00\x00\x00\x00\x00', check=False)
        out, _, status = result.stdout.rpartition(b'\n')
        return int(status or 0), out

    # ── mail ────────────────────────────────────────────────────────────────

    # The two Mailpit readers, this one and the e2e harness's mailbox.py, run
    # in different Python environments over different transports, so each has
    # its own; they search and match the same way: the recipient quoted, the
    # subject whole.
    def messages(self, recipient):
        """Every message Mailpit holds for recipient, oldest first, each with
        its subject and HTML body."""
        query = urllib.parse.urlencode({'query': f'to:"{recipient}"'})
        listing = json.loads(self.curl(MAILPIT + '/api/v1/search?' + query, host='mailpit'))
        found = []
        for summary in reversed(listing.get('messages', [])):
            message = json.loads(self.curl(MAILPIT + '/api/v1/message/' + summary['ID'], host='mailpit'))
            found.append({'subject': message['Subject'], 'html': message.get('HTML', '')})
        return found

    def wait_for_message(self, recipient, subject, seconds=60):
        """The first message to recipient whose subject is exactly subject."""
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            for message in self.messages(recipient):
                if message['subject'] == subject:
                    return message
            time.sleep(.5)
        raise AssertionError(f'no message {subject!r} to {recipient}: '
                             f'{[m["subject"] for m in self.messages(recipient)]}')
