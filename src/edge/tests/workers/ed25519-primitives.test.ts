import { nobleEd25519 } from '../../src/ed25519-noble.js';
import { webCryptoEd25519 } from '../../src/ed25519.js';
import { describeEd25519Primitive } from '../helpers/ed25519-primitive.js';

// On workerd, the Cloudflare Workers runtime: WebCrypto Ed25519 is the primitive
// the Cloudflare entry verifies with. @noble/ed25519 is pure JavaScript and
// keeps the same contract here as on Node.
describeEd25519Primitive('webCryptoEd25519 on workerd', webCryptoEd25519);
describeEd25519Primitive('nobleEd25519 on workerd', nobleEd25519);
