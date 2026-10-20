#!/usr/bin/env python3
"""Create the publisher instance through Zitadel's System API and provision both
instances of one real Zitadel; read every result back.

Requires Docker. zitadel_testbed.py describes the Zitadel and how the scripts
run; here Zitadel trusts the System API user with the key local-system-key.sh
keeps, and sends every instance's email to Mailpit.

  1. instance.sh with a token signed by another key, and with a token for
     another audience: the System API refuses both, no instance is created and
     no bootstrap token is written.
  2. instance.sh with the right token: the instance is created, named
     fora-publishers at its own address, and its bootstrap token is written
     readable by root only.
  3. instance.sh again, with no System API token at all: the stored token opens
     the instance, so nothing is created and the System API is not needed.
  4. instance.sh after the bootstrap token is lost: the instance is found, not
     created again, and the run stops and says how to recover.
  5. publishers.sh, and bootstrap.sh for the developer instance. The publisher
     instance gets only the console's client, stored with its sign-in and
     sign-out addresses, whose secret works; the login policy; the
     publishers' look (FORA website colors with native dark controls, the wordmark,
     white FORA icon and Instrument Sans byte for byte, English only, titles and
     email texts); and zadmin, an IAM_OWNER and ORG_OWNER who signs in with
     the given password. The developer instance keeps the developers' look
     and has no console client.
  6. publishers.sh again, with another admin password: every step reads back
     as unchanged, no event is written with the instance's token, and
     zadmin's password is the first one.
  7. The four emails a person can get, from each instance: FORA subjects,
     the instance's logo, primary color and own host. Both audiences' messages
     render the recipient, username and code, with FORA copy and action labels.
  8. A recovery window: Zitadel restarted with the System API user also
     IAM_OWNER of the publisher instance, in the shape compose-stack renders
     for zitadel_system_api_recovery_instance_id. With the token file gone,
     instance.sh mints a new token, the old one stops working, no second
     instance appears, and publishers.sh works with the new token. Then the
     window is closed again.

A second test puts Caddy in front of Zitadel with the Zitadel site the module
renders, and sends every route that needs a system permission through it with
the System API token: each one answers 404 from Caddy, while Zitadel itself
serves it, and the rest of Zitadel stays reachable through Caddy.
"""
from html import unescape
import json
import re
import time
import unittest
import urllib.parse

import zitadel_testbed as tb

PAT_FILE = '/publishers/bootstrap.pat'
ADMIN_PASSWORD = 'Publisher-Admin-1!'
CONSOLE = 'http://console.example'

# Publisher emails use the console panel background; sign-in uses the dark page
# background. Both share the wordmark, white FORA icon and Instrument Sans.
PALETTE = {'primaryColor': '#4f6df5', 'backgroundColor': '#0a0a0f', 'warnColor': '#ff8a80', 'fontColor': '#e8e8f0'}
PUBLISHER_LOOK = {
    'colors': {**PALETTE, 'backgroundColor': '#12121a', **{key + 'Dark': value for key, value in PALETTE.items()}},
    'assets': {'logo': tb.mark('fora-wordmark.png'), 'logo/dark': tb.mark('fora-wordmark.png'),
               'icon': tb.mark('fora-mark-white.png'), 'icon/dark': tb.mark('fora-mark-white.png'),
               'font': tb.mark('instrument-sans.ttf')},
    'languages': ['en'],
    'titles': ['FORA Publishers'] * 3,
    'emails': tb.emails('FORA Publishers'),
}
# Who receives each of the four emails in assert_emails.
RECIPIENTS = {'init': 'init', 'verifyemail': 'verify', 'passwordreset': 'reset', 'password_change': 'reset'}

# Every route Zitadel v3.4.9 serves that a system permission opens: the System
# API, and the system-wide features of the FeatureService (both versions), over
# REST and over gRPC-Web. The System API user holds SYSTEM_OWNER, so Caddy must
# refuse each one on the public hostnames.
SYSTEM_REST = [('POST', '/system/v1/instances/_search'), ('GET', '/v2/features/system'),
               ('PUT', '/v2/features/system'), ('DELETE', '/v2/features/system'),
               ('GET', '/v2beta/features/system'), ('PUT', '/v2beta/features/system'),
               ('DELETE', '/v2beta/features/system')]
SYSTEM_GRPC = ['/zitadel.system.v1.SystemService/ListInstances'] + [
    f'/zitadel.feature.{version}.FeatureService/{method}SystemFeatures'
    for version in ('v2', 'v2beta') for method in ('Set', 'Reset', 'Get')]
# The same routes spelled the ways a request can try to slip past a path match.
SYSTEM_VARIANTS = [('POST', '//system/v1/instances/_search'), ('POST', '/%73ystem/v1/instances/_search'),
                   ('POST', '/SYSTEM/v1/instances/_search'), ('PUT', '/v2/features/system/'),
                   ('PUT', '/V2/Features/SYSTEM'), ('PUT', '/v2/features/%73ystem')]


class ZitadelInstances(unittest.TestCase):
    bed: tb.Testbed
    started: float

    @classmethod
    def setUpClass(cls):
        cls.started = time.monotonic()
        cls.bed = tb.Testbed(cls.addClassCleanup, system_api=True, mail=True)
        cls.bed.start()
        cls.bed.system_key()

    def instance_step(self, token_file):
        return self.bed.run('instance.sh', {'ZITADEL_INSTANCE_HOST': tb.PUBLISHERS, 'ZITADEL_PAT_FILE': PAT_FILE,
                                            'ZITADEL_SYSTEM_TOKEN_FILE': token_file})

    def publishers_run(self, admin_password):
        """publishers.sh, as every stack runs the publishers' side: the instance
        step, then the bootstrap."""
        status, out, err = self.bed.run('publishers.sh', {
            'ZITADEL_BASE_URL': tb.ZITADEL, 'ZITADEL_INSTANCE_HOST': tb.PUBLISHERS, 'ZITADEL_PAT_FILE': PAT_FILE,
            'ZITADEL_ISSUER': 'http://zitadel-publishers:8080', 'CONSOLE_PUBLIC_ORIGIN': CONSOLE,
            'CONSOLE_OUT_DIR': '/console-oidc', 'ZITADEL_ADMIN_PASSWORD': admin_password, 'IMPORT_DEMO_USERS': '0'})
        self.assertEqual(status, 0, err)
        return out, err

    def developers_run(self):
        status, _, err = self.bed.run('bootstrap.sh', tb.developers_env('http://identity', OUT_DIR='/bootstrap',
                                                                        IMPORT_DEMO_USERS='0'))
        self.assertEqual(status, 0, err)

    def signs_in(self, password):
        """Whether zadmin's password check passes on the publisher instance."""
        status, _ = self.bed.call(tb.PUBLISHERS, self.bed.publisher_pat(), 'POST', '/v2/sessions',
                                  {'checks': {'user': {'loginName': 'zadmin'}, 'password': {'password': password}}})
        return status == 201

    def roles_of(self, path, login_name):
        found = self.bed.api(tb.PUBLISHERS, self.bed.publisher_pat(), 'POST', path, {}).get('result', [])
        return [role for member in found if member.get('preferredLoginName') == login_name
                for role in member.get('roles', [])]

    def test_the_publisher_instance_is_created_and_provisioned(self):
        # 1. Refused System API tokens: nothing created, no token written.
        self.bed.shell('openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out /tmp/other.pem 2>/dev/null && '
                       'sh /zitadel/system-token.sh /tmp/other.pem http://zitadel:8080 > /systemapi/wrong-key.token && '
                       'sh /zitadel/system-token.sh /systemapi/private.pem http://other:8080 > /systemapi/wrong-audience.token',
                       image=tb.OPENSSL_IMAGE)
        for token in ('/systemapi/wrong-key.token', '/systemapi/wrong-audience.token'):
            status, _, err = self.instance_step(token)
            self.assertEqual(status, 1, err)
            self.assertIn('System API refused the token', err)
            self.assertIsNone(self.bed.read(PAT_FILE), token)
        self.assertEqual(self.bed.instances_at(tb.PUBLISHERS), [])

        # 2. The right token: created, and its bootstrap token kept for root.
        self.bed.system_key()
        status, _, err = self.instance_step('/systemapi/token')
        self.assertEqual(status, 0, err)
        self.assertIn('created instance fora-publishers', err)
        self.assertEqual(self.bed.instances_at(tb.PUBLISHERS), ['fora-publishers'])
        self.assertEqual(self.bed.mode(PAT_FILE), '600')

        # 3. Again without any System API token: the stored token is enough.
        status, _, err = self.instance_step('/systemapi/absent.token')
        self.assertEqual(status, 0, err)
        self.assertIn('already exists and its token works; nothing to change', err)

        # 4. The bootstrap token lost: found, not created twice, and the run
        # says where recovery is described.
        self.bed.shell(f'mv {PAT_FILE} /publishers/kept.pat')
        self.bed.system_key()
        status, _, err = self.instance_step('/systemapi/token')
        self.assertEqual(status, 1, err)
        self.assertIn("Recovering the publisher instance's token", err)
        self.assertEqual(self.bed.instances_at(tb.PUBLISHERS), ['fora-publishers'])
        self.bed.shell(f'mv /publishers/kept.pat {PAT_FILE}')

        # 5. Both sides provisioned.
        self.developers_run()
        _, err = self.publishers_run(ADMIN_PASSWORD)
        pat = self.bed.publisher_pat()
        self.assertIn('already exists and its token works; nothing to change', err)
        apps = self.bed.apps(tb.PUBLISHERS, pat)
        self.assertEqual(sorted(apps), ['fora-console'])
        # The console's sign-in and sign-out addresses, as Zitadel stored them:
        # http here, so Development mode on.
        tb.assert_app(self, apps['fora-console'], [CONSOLE + '/auth/callback'], [CONSOLE + '/signed-out'], True)
        tb.assert_sign_in(self, self.bed, tb.PUBLISHERS, apps['fora-console'], [CONSOLE + '/auth/callback'],
                          refused=['http://elsewhere.example/auth/callback'])
        client_id = self.bed.read('/console-oidc/console_client_id')
        self.assertEqual(client_id, apps['fora-console']['clientId'])
        self.assertNotEqual(self.bed.token_error(tb.PUBLISHERS, client_id, self.bed.read('/console-oidc/console_client_secret')),
                            'invalid_client')
        # The console's client belongs to this instance only.
        self.assertEqual(self.bed.token_error(tb.DEVELOPERS, client_id, self.bed.read('/console-oidc/console_client_secret')),
                         'invalid_client')
        self.assertEqual(sorted(self.bed.apps(tb.DEVELOPERS, self.bed.developer_pat())), ['fora-identity'])
        policy = self.bed.api(tb.PUBLISHERS, pat, 'GET', '/management/v1/policies/login')['policy']
        self.assertIs(policy.get('allowRegister'), True, policy)
        self.assertIs(policy.get('hidePasswordReset', False), False, policy)
        self.assertIs(policy.get('forceMfa', False), False, policy)
        self.assertEqual(self.bed.look(tb.PUBLISHERS, pat), PUBLISHER_LOOK)
        for host, token, client, callback, theme, mode, saved in (
            (tb.PUBLISHERS, pat, client_id, CONSOLE + '/auth/callback', 'DARK', '2', 'light'),
            (tb.DEVELOPERS, self.bed.developer_pat(), self.bed.read('/bootstrap/identity_client_id'),
             'http://identity/callback', 'LIGHT', '1', 'dark'),
        ):
            label = self.bed.api(host, token, 'GET', '/admin/v1/policies/label')['policy']
            self.assertEqual(label.get('themeMode'), 'THEME_MODE_' + theme)
            # A saved browser preference must not change either audience's theme.
            query = urllib.parse.urlencode({'client_id': client, 'redirect_uri': callback,
                                            'response_type': 'code', 'scope': 'openid'})
            login_url = self.bed.curl('-c', '/tmp/login-cookies', '-o', '/dev/null', '-w', '%{redirect_url}',
                                      tb.ZITADEL + '/oauth/v2/authorize?' + query, host=host)
            login_path = urllib.parse.urlsplit(login_url)
            html = self.bed.curl('-b', '/tmp/login-cookies', '-c', '/tmp/login-cookies', '-H', 'Cookie: mode=' + saved,
                                 tb.ZITADEL + login_path.path + '?' + login_path.query, host=host)
            self.assertIn('name="loginName"', html)
            self.assertIn('FORA Publishers' if host == tb.PUBLISHERS else 'FORA Developers', html)
            self.assertIn('lgn-header', html)
            self.assertRegex(html, rf'<html[^>]*class="[^"]*lgn-{theme.lower()}-theme')
            self.assertIn(f'data-theme-mode="{mode}"', html)
            fields = {name: unescape(value) for name, value in
                      re.findall(r'<input[^>]*type="hidden"[^>]*name="([^"]+)"[^>]*value="([^"]*)"', html)}
            form = urllib.parse.urlencode({**fields, 'register': 'true'})
            registration = self.bed.curl('-L', '-b', '/tmp/login-cookies', '-c', '/tmp/login-cookies',
                                          '-d', form, tb.ZITADEL + '/ui/login/loginname', host=host)
            self.assertIn('FORA Publishers' if host == tb.PUBLISHERS else 'FORA Developers', registration)
            self.assertIn('lgn-header', registration)
            self.assertIn('name="email"', registration)
            self.assertIn('name="register-password"', registration)
            self.assertIn('name="register-password-confirmation"', registration)
            self.assertIn(f'data-theme-mode="{mode}"', registration)
        self.assertEqual(self.bed.look(tb.DEVELOPERS, self.bed.developer_pat())['titles'], ['FORA Developers'] * 3)
        self.assertEqual(self.roles_of('/admin/v1/members/_search', 'zadmin'), ['IAM_OWNER'])
        self.assertEqual(self.roles_of('/management/v1/orgs/me/members/_search', 'zadmin'), ['ORG_OWNER'])
        self.assertTrue(self.signs_in(ADMIN_PASSWORD), 'zadmin cannot sign in with the given password')

        # 6. Again, with another admin password: nothing changes, nothing is
        # written, and the admin keeps the first password.
        editor = self.bed.user_id(tb.PUBLISHERS, pat)
        events = self.bed.events_by(tb.PUBLISHERS, pat, editor)
        _, err = self.publishers_run('Another-Admin-9!')
        for line in ('app fora-console already existed with these settings', 'app fora-console kept its secret',
                     'label colors unchanged', 'font unchanged (instrument-sans.ttf)', 'label policy unchanged',
                     'page titles unchanged', 'password_change email unchanged'):
            self.assertIn(line, err)
        self.assertEqual(self.bed.events_by(tb.PUBLISHERS, pat, editor), events, 'an unchanged run wrote events')
        self.assertTrue(self.signs_in(ADMIN_PASSWORD), "a re-run changed zadmin's password")
        self.assertFalse(self.signs_in('Another-Admin-9!'), "a re-run changed zadmin's password")

        # 7. Each instance's four emails.
        for host, token, side, primary in ((tb.PUBLISHERS, pat, 'FORA Publishers', '#4f6df5'),
                                           (tb.DEVELOPERS, self.bed.developer_pat(), 'FORA Developers', '#4f6df5')):
            self.assert_emails(host, token, side, primary)

        # 8. A recovery window, opened and closed through ZITADEL_SYSTEMAPIUSERS.
        instance_id = self.bed.api(tb.PUBLISHERS, pat, 'GET', '/admin/v1/instances/me')['instance']['id']
        self.bed.restart_zitadel(json.dumps({'fora-instance-admin': {
            'Path': '/systemapi/public.pem',
            'Memberships': [{'MemberType': 'System', 'Roles': ['SYSTEM_OWNER']},
                            {'MemberType': 'IAM', 'Roles': ['IAM_OWNER'], 'AggregateID': instance_id}]}}))
        self.bed.shell(f'rm {PAT_FILE}')
        self.bed.system_key()
        status, _, err = self.instance_step('/systemapi/token')
        self.assertEqual(status, 0, err)
        self.assertIn('recovered a token for fora-bootstrap', err)
        self.assertIn('deleted the earlier token', err)
        recovered = self.bed.publisher_pat()
        self.assertNotEqual(recovered, pat)
        self.assertEqual(self.bed.mode(PAT_FILE), '600')
        self.assertEqual(self.bed.status_of(tb.PUBLISHERS, recovered, 'GET', '/management/v1/orgs/me'), 200)
        self.assertEqual(self.bed.status_of(tb.PUBLISHERS, pat, 'GET', '/management/v1/orgs/me'), 401,
                         'the token the recovery replaced still works')
        self.assertEqual(self.bed.instances_at(tb.PUBLISHERS), ['fora-publishers'])
        _, err = self.publishers_run(ADMIN_PASSWORD)
        self.assertIn('app fora-console kept its secret', err)
        self.bed.restart_zitadel(tb.SYSTEM_API_USERS)
        self.assertEqual(self.bed.status_of(tb.PUBLISHERS, recovered, 'GET', '/management/v1/orgs/me'), 200)
        print(f'\nreal Zitadel instances round trip: {time.monotonic() - self.started:.0f}s', flush=True)

    def test_caddy_keeps_the_system_routes_off_the_public_hostnames(self):
        self.bed.start_caddy()
        css_path = '/ui/login/resources/themes/zitadel/css/zitadel.css'
        publisher_css = self.bed.curl(tb.CADDY + css_path, host=tb.PUBLISHERS)
        self.assertEqual(publisher_css, (tb.REPO / 'deploy/zitadel/publisher-login.css').read_text())
        upstream_css = self.bed.curl(tb.ZITADEL + css_path)
        self.assertEqual(self.bed.curl(tb.CADDY + css_path),
                         (tb.REPO / 'deploy/zitadel/developer-login.css').read_text())
        for host in (tb.DEVELOPERS, tb.PUBLISHERS):
            self.assertEqual(self.bed.curl(tb.CADDY + css_path + '?fora-original=1', host=host), upstream_css)
        self.bed.system_key()
        token = self.bed.read('/systemapi/token')
        developer = self.bed.developer_pat()
        # Zitadel itself serves each route: a 404 through Caddy is the block,
        # not a route that does not exist.
        self.assertEqual(self.bed.answer_at(tb.ZITADEL, '', token, 'POST', '/system/v1/instances/_search', {})[0], 200)
        for method, path in (('GET', '/v2/features/system'), ('PUT', '/v2/features/system'),
                             ('GET', '/v2beta/features/system'), ('PUT', '/v2beta/features/system')):
            body: dict | None = {} if method == 'PUT' else None
            self.assertNotEqual(self.bed.answer_at(tb.ZITADEL, tb.DEVELOPERS, token, method, path, body)[0], 404, path)
        for path in SYSTEM_GRPC:
            self.assertNotEqual(self.bed.grpc_web_answer(tb.ZITADEL, tb.DEVELOPERS, token, path)[0], 404, path)
        # Through Caddy, each one, in each spelling, gets Caddy's own answer: 404
        # with an empty body. Zitadel's own 404 carries a body, so a spelling
        # Caddy let through would show here even where Zitadel serves nothing.
        for method, path in SYSTEM_REST + SYSTEM_VARIANTS:
            body = {} if method in ('POST', 'PUT') else None
            self.assertEqual(self.bed.answer_at(tb.CADDY, tb.DEVELOPERS, token, method, path, body), (404, ''),
                             f'{method} {path} passed Caddy')
        for path in SYSTEM_GRPC:
            self.assertEqual(self.bed.grpc_web_answer(tb.CADDY, tb.DEVELOPERS, token, path), (404, b''),
                             f'{path} passed Caddy')
        # Everything else still passes: sign-in discovery, the instance's own
        # APIs, and the FeatureService's instance-level features.
        for path in ('/.well-known/openid-configuration', '/admin/v1/instances/me', '/v2/features/instance'):
            self.assertEqual(self.bed.answer_at(tb.CADDY, tb.DEVELOPERS, developer, 'GET', path)[0], 200, path)

    def assert_emails(self, host, pat, side, primary):
        """Sends the four emails from one instance and checks what arrives."""
        domain = host + '.example'
        # Sign-up's email: a person added without a password gets the
        # initialization code.
        self.bed.api(host, pat, 'POST', '/management/v1/users/human', {
            'userName': 'init@' + domain, 'profile': {'firstName': 'Ina', 'lastName': 'Init'},
            'email': {'email': 'init@' + domain, 'isEmailVerified': False}})
        self.bed.api(host, pat, 'POST', '/v2/users/human', {
            'username': 'verify@' + domain, 'profile': {'givenName': 'Vera', 'familyName': 'Verify'},
            'email': {'email': 'verify@' + domain, 'sendCode': {}},
            'password': {'password': 'Verify-Pass-1!', 'changeRequired': False}})
        user = self.bed.api(host, pat, 'POST', '/v2/users/human', {
            'username': 'reset@' + domain, 'profile': {'givenName': 'Remy', 'familyName': 'Reset'},
            'email': {'email': 'reset@' + domain, 'isVerified': True},
            'password': {'password': 'Reset-Pass-1!', 'changeRequired': False}})['userId']
        self.bed.api(host, pat, 'POST', f'/v2/users/{user}/password_reset', {'sendLink': {}})
        self.bed.api(host, pat, 'POST', f'/v2/users/{user}/password',
                     {'newPassword': {'password': 'Reset-Pass-2!', 'changeRequired': False}})
        logo = self.bed.api(host, pat, 'GET', '/admin/v1/policies/label')['policy']['logoUrl']
        for kind, text in tb.emails(side).items():
            message = self.bed.wait_for_message(f'{RECIPIENTS[kind]}@{domain}', text['subject'])
            html = message['html']
            self.assertIn(f'src="{logo}"', html, message['subject'])
            self.assertIn(primary, html.lower(), message['subject'])
            hosts = {urllib.parse.urlsplit(url).hostname for url in re.findall(r'(?:href|src)="(https?://[^"]+)"', html)}
            self.assertEqual(hosts, {host}, message['subject'])
            self.assertIn('background-color:' + ('#12121a' if host == tb.PUBLISHERS else '#fafafa'), html)
            self.assertIn('color:' + ('#e8e8f0' if host == tb.PUBLISHERS else '#12121a'), html)
            self.assertIn('font-size:16px', html)
            self.assertIn('font-face', html)
            self.assertIn('/policy/label/font-', html)
            visible = unescape(re.sub(r'<[^>]+>', '', html[html.index('<body'):]))
            self.assertIn('FORA publisher console' if host == tb.PUBLISHERS else 'FORA developer', visible)
            self.assertNotIn('FORA Developers' if host == tb.PUBLISHERS else 'FORA Publishers', visible)
            self.assertNotIn('zitadel', visible.replace(domain, '').lower())
            self.assertNotIn('{{.', visible)
            self.assertIn({'init': 'Hello Ina Init,', 'verifyemail': 'Hello Vera Verify,',
                           'passwordreset': 'Hello Remy Reset,', 'password_change': 'Hello Remy Reset,'}[kind], visible)
            self.assertIn({'init': 'Set up your account', 'verifyemail': 'Verify email address',
                           'passwordreset': 'Reset your password', 'password_change': 'Go to sign-in'}[kind], visible)
            if kind == 'init':
                self.assertIn('init@' + domain, visible)
                self.assertIn('confirm your email address', visible)
            if kind == 'password_change':
                self.assertIn('reset your password immediately', visible)
            else:
                links = [urllib.parse.urlsplit(unescape(url)) for url in re.findall(r'href="(https?://[^"]+)"', html)]
                codes = [urllib.parse.parse_qs(link.query)['code'][0] for link in links
                         if 'code' in urllib.parse.parse_qs(link.query)]
                self.assertTrue(codes, 'action link lost its code')
                self.assertIn('code: ' + codes[0], visible)
                self.assertIn('ignore this email', visible)


if __name__ == '__main__':
    unittest.main()
