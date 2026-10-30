// The settings an earlier release read to make the discovery document and the
// RSL file itself. This release reads none of them, so a Worker or a Fastly
// configuration store that still carries them must behave as if they were
// absent. Each value is one the earlier release would show in a document built
// from it, or refuse outright: EXCHANGES_JSON is not JSON, which once made every
// request fail.
export const EARLIER_RELEASE_SETTINGS: Readonly<Record<string, string>> = Object.freeze({
  PROVIDER: 'built-from-settings.example',
  EXCHANGES_JSON: 'not json',
  CATALOG_CONTRIBUTORS_JSON: '[{"domain":"contributor.example","relationship":"operator"}]',
  RSL_BODY: '# an RSL body from the settings',
});
