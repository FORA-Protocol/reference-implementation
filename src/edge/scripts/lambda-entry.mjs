// The Lambda@Edge package layout: the release bundle under its release name,
// and an index.mjs beside it that assigns the settings and re-exports the
// handler. Lambda loads index.mjs for the handler string "index.handler".
//
// The file is rendered from edgerelease/lambda-entry.mjs.tmpl, the template
// the console renders for a publisher's download (edgerelease.LambdaEntry).
// Both renderings are held to edgerelease/testdata/lambda-index.mjs.golden, so a
// deployment uploads exactly the file publishers install.
//
// The re-export is static, and a module's static imports and exports are
// evaluated before its body runs. So lambda-edge.mjs is loaded BEFORE the
// assignment. That is safe only because the bundle reads process.env on its
// first invocation and not at load: getHandler in src/entries/aws-lambda.ts
// parses the environment the first time the handler is called. If the bundle
// ever read its settings at load, this entry would hand it an empty
// environment; tests/node/release-package.test.ts fails if that happens.

import { readFileSync } from 'node:fs';

import { lambdaTarget } from './release-targets.mjs';

export const LAMBDA_ENTRY = 'index.mjs';
export const LAMBDA_BUNDLE = lambdaTarget.name;

const TEMPLATE = readFileSync(
  new URL('../edgerelease/lambda-entry.mjs.tmpl', import.meta.url),
  'utf8',
);

// lambdaEntrySource renders index.mjs. The settings are written as JSON with
// sorted keys and a two-space indent, so a value reaches the runtime as the
// string it is and never becomes part of the program. The domain goes into a
// comment, so it must be one printable token with no line break that could end
// the comment. The template is filled in one pass: a value that contains a
// placeholder is written as it is.
export function lambdaEntrySource(domain, settings) {
  if (!/^[\x21-\x7e]+$/.test(domain)) {
    throw new Error(`the domain ${JSON.stringify(domain)} is not a bare hostname`);
  }
  const sorted = Object.fromEntries(
    Object.keys(settings)
      .sort()
      .map((k) => [k, settings[k]]),
  );
  const values = {
    DOMAIN: domain,
    BUNDLE: LAMBDA_BUNDLE,
    SETTINGS: JSON.stringify(sorted, null, 2),
  };
  return TEMPLATE.replace(/\{\{(DOMAIN|BUNDLE|SETTINGS)\}\}/g, (_, key) => values[key]);
}
