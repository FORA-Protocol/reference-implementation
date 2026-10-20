#!/usr/bin/env python3
"""Run deploy/zitadel/bootstrap/bootstrap.sh against the developer instance of a
real Zitadel; read the apps and the look back.

Requires Docker. zitadel_testbed.py describes the Zitadel and how the script
runs; this test needs neither the System API nor mail.

The developer instance, with ZITADEL_INSTANCE_ROLE=developers; each run is read
back over the management and admin APIs and checked at the token and authorize
endpoints:
  1. https origins on a fresh instance that still holds a `fora-console`
     client from before publishers had an instance of their own: the Identity
     Service's client is created with Development mode off, the console's is
     removed, and nothing is written for the console. The look reads back as
     the developers' side: light colors, Instrument Sans and the wordmark
     byte for byte, English only, the three page titles and the four emails'
     titles and subjects.
  2. a different identity origin: the client's addresses are updated, and its
     stored secret is kept, because it still works.
  3. the same inputs again: Zitadel answers the update with "No changes", the
     secret is kept again, every step reads back as unchanged, and the run
     writes no event at all.
  4. the e2e inputs, plain http: Development mode on again.
  5. the https origin of run 1 again: a client left with an http address and
     Development mode on is brought back to https with it off.
  6. ROTATE_CLIENT_SECRET=1: a new secret, and the old one is refused.
  7. the publishers role with this instance's token: refused before any write.
  8. a font removed and the policy activated by hand: the next run
     restores Instrument Sans.
"""
import time
import unittest

import zitadel_testbed as tb

HTTPS = 'https://id.example'
MOVED = 'https://identity.example'
E2E = 'http://identity'
HOST = tb.DEVELOPERS

# The developers' side keeps a light background with the shared FORA branding.
DEVELOPER_LOOK = {
    'colors': {'primaryColor': '#4f6df5', 'backgroundColor': '#fafafa', 'warnColor': '#cd3d56', 'fontColor': '#12121a',
               'primaryColorDark': '#2073c4', 'backgroundColorDark': '#111827', 'warnColorDark': '#ff3b5b',
               'fontColorDark': '#ffffff'},
    'assets': {'logo': tb.mark('fora-wordmark-dark.png'), 'logo/dark': tb.mark('fora-wordmark.png'),
               'icon': tb.mark('fora-mark-black.png'), 'icon/dark': tb.mark('fora-mark-white.png'),
               'font': tb.mark('instrument-sans.ttf')},
    'languages': ['en'],
    'titles': ['FORA Developers'] * 3,
    'emails': tb.emails('FORA Developers'),
}


class ZitadelBootstrapReconcile(unittest.TestCase):
    bed: tb.Testbed
    started: float

    @classmethod
    def setUpClass(cls):
        cls.started = time.monotonic()
        cls.bed = tb.Testbed(cls.addClassCleanup)
        cls.bed.start()

    def pat(self):
        return self.bed.developer_pat()

    def bootstrap(self, identity, **extra):
        """Run the real script, developers role; returns (status, stdout, stderr)."""
        return self.bed.run('bootstrap.sh', tb.developers_env(identity, **extra))

    def deployment_run(self, identity, **extra):
        """A deployment's inputs, as bootstrap-identity.sh passes them."""
        status, out, err = self.bootstrap(identity, OUT_DIR='/bootstrap', IMPORT_DEMO_USERS='0', **extra)
        self.assertEqual(status, 0, err)
        return out, err

    def apps(self):
        return self.bed.apps(HOST, self.pat())

    def assert_app(self, config, redirects, logouts, dev_mode):
        tb.assert_app(self, config, redirects, logouts, dev_mode)

    def assert_secret_works(self, client_id, secret):
        self.assertNotEqual(self.bed.token_error(HOST, client_id, secret), 'invalid_client', 'the secret is refused')

    def assert_secret_refused(self, client_id, secret):
        self.assertEqual(self.bed.token_error(HOST, client_id, secret), 'invalid_client', 'an old secret still works')

    def assert_sign_in(self, config, redirects, refused=()):
        tb.assert_sign_in(self, self.bed, HOST, config, redirects, refused)

    def credentials(self, directory, client):
        return self.bed.read(f'{directory}/{client}_client_id'), self.bed.read(f'{directory}/{client}_client_secret')

    def leave_the_old_layout(self):
        """What a stack kept from before publishers had an instance of their own:
        project fora with a fora-console client, and that client's files in the
        shared volume."""
        project = self.bed.api(HOST, self.pat(), 'POST', '/management/v1/projects',
                               {'name': 'fora', 'projectRoleAssertion': True, 'projectRoleCheck': False})['id']
        self.bed.api(HOST, self.pat(), 'POST', f'/management/v1/projects/{project}/apps/oidc', {
            'name': 'fora-console', 'version': 'OIDC_VERSION_1_0', 'redirectUris': ['https://console.example/auth/callback'],
            'responseTypes': ['OIDC_RESPONSE_TYPE_CODE'], 'grantTypes': ['OIDC_GRANT_TYPE_AUTHORIZATION_CODE'],
            'appType': 'OIDC_APP_TYPE_WEB', 'authMethodType': 'OIDC_AUTH_METHOD_TYPE_BASIC'})
        self.bed.shell('echo old-console > /bootstrap/console_client_id && echo old-secret > /bootstrap/console_client_secret')

    def test_the_developer_instance_is_brought_to_the_given_addresses(self):
        self.leave_the_old_layout()
        self.assertEqual(sorted(self.apps()), ['fora-console'])

        # 1. https origin: the Identity Service's client is created with
        # Development mode off; the console's client and its files are gone;
        # the instance looks like the developers' side.
        _, err = self.deployment_run(HTTPS)
        apps = self.apps()
        self.assertEqual(sorted(apps), ['fora-identity'])
        self.assertIn('app fora-console removed', err)
        for line in ('languages applied (English only)', 'label policy applied (activated)',
                     'page titles applied (FORA Developers)', 'init email applied (Welcome to FORA Developers)'):
            self.assertIn(line, err)
        self.assertEqual(self.bed.look(HOST, self.pat()), DEVELOPER_LOOK)
        self.assertEqual(self.credentials('/bootstrap', 'console'), (None, None))
        self.assert_app(apps['fora-identity'], [HTTPS + '/callback'], [], False)
        identity_id, secret = self.credentials('/bootstrap', 'identity')
        self.assertEqual(identity_id, apps['fora-identity']['clientId'])
        self.assert_secret_works(identity_id, secret)
        self.assert_sign_in(apps['fora-identity'], [HTTPS + '/callback'], refused=[E2E + '/callback'])

        # 2. A different origin: the same client, new addresses, and the stored
        # secret kept because the token endpoint still accepts it.
        _, err = self.deployment_run(MOVED)
        self.assertIn('app fora-identity already existed; updated its settings', err)
        self.assertIn('app fora-identity kept its secret', err)
        apps = self.apps()
        self.assert_app(apps['fora-identity'], [MOVED + '/callback'], [], False)
        self.assertEqual(self.credentials('/bootstrap', 'identity'), (identity_id, secret))
        self.assert_secret_works(identity_id, secret)
        self.assert_sign_in(apps['fora-identity'], [MOVED + '/callback'], refused=[HTTPS + '/callback'])

        # 3. The same inputs again: Zitadel answers the update with "No changes",
        # the secret is kept, the files are left alone, nothing else to remove,
        # and Zitadel holds no new event written with the script's token.
        editor = self.bed.user_id(HOST, self.pat())
        events = self.bed.events_by(HOST, self.pat(), editor)
        _, err = self.deployment_run(MOVED)
        for line in ('app fora-identity already existed with these settings', 'app fora-identity kept its secret',
                     'identity_client_secret unchanged', 'no fora-console app in this instance',
                     # The look, read back from Zitadel's own answers: nothing to change.
                     'languages unchanged', 'default language unchanged', 'label colors unchanged',
                     'logo unchanged', 'logo/dark unchanged', 'icon unchanged', 'icon/dark unchanged',
                     'font unchanged', 'label policy unchanged', 'page titles unchanged', 'init email unchanged',
                     'verifyemail email unchanged', 'passwordreset email unchanged', 'password_change email unchanged'):
            self.assertIn(line, err)
        self.assertEqual(self.bed.events_by(HOST, self.pat(), editor), events, 'an unchanged run wrote events')
        self.assertEqual(self.apps(), apps)

        # 4. The e2e inputs: plain http, so Development mode on, and the exports
        # are e2e's.
        status, out, err = self.bootstrap(E2E)
        self.assertEqual(status, 0, err)
        apps = self.apps()
        self.assert_app(apps['fora-identity'], [E2E + '/callback'], [], True)
        self.assertIn(f'export IDENTITY_OIDC_CLIENT_ID={identity_id}\n', out)
        self.assert_sign_in(apps['fora-identity'], [E2E + '/callback'])

        # 5. Back to https: a client an http run left in Development mode is
        # brought back to https with the mode off.
        self.deployment_run(HTTPS)
        apps = self.apps()
        self.assert_app(apps['fora-identity'], [HTTPS + '/callback'], [], False)
        self.assert_sign_in(apps['fora-identity'], [HTTPS + '/callback'], refused=[E2E + '/callback'])

        # 6. Rotation asked for: a new secret that works, and the old one refused.
        _, err = self.deployment_run(HTTPS, ROTATE_CLIENT_SECRET='1')
        self.assertIn('app fora-identity got a new secret (rotation requested)', err)
        rotated = self.credentials('/bootstrap', 'identity')[1]
        self.assertNotEqual(rotated, secret)
        self.assert_secret_works(identity_id, rotated)
        self.assert_secret_refused(identity_id, secret)

        # 7. The publishers role aimed at this instance: the name check stops it
        # before anything is written.
        events = self.bed.events_by(HOST, self.pat(), editor)
        status, _, err = self.bootstrap(HTTPS, ZITADEL_INSTANCE_ROLE='publishers', CONSOLE_PUBLIC_ORIGIN='https://console.example',
                                        CONSOLE_OUT_DIR='/console-oidc', ZITADEL_ADMIN_PASSWORD='Publisher-Admin-1!')
        self.assertEqual(status, 2, err)
        self.assertIn("the publishers role needs the instance 'fora-publishers', but zitadel is ", err)
        self.assertEqual(self.bed.events_by(HOST, self.pat(), editor), events, 'a refused run wrote events')
        self.assertEqual(sorted(self.apps()), ['fora-identity'])

        # 8. A deleted font is restored by the next run.
        self.bed.api(HOST, self.pat(), 'DELETE', '/admin/v1/policies/label/font')
        self.bed.api(HOST, self.pat(), 'POST', '/admin/v1/policies/label/_activate', {})
        self.assertIsNone(self.bed.look(HOST, self.pat())['assets']['font'])
        _, err = self.deployment_run(HTTPS)
        self.assertIn('font applied (instrument-sans.ttf)', err)
        self.assertIn('label policy applied (activated)', err)
        self.assertEqual(self.bed.look(HOST, self.pat()), DEVELOPER_LOOK)
        print(f'\nreal Zitadel bootstrap round trip: {time.monotonic() - self.started:.0f}s', flush=True)


if __name__ == '__main__':
    unittest.main()
