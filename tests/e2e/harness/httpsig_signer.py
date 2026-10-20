"""The harness's request signers, over the keyfiles ``scripts/gen-e2e-keys.sh`` writes.

Every signer here is the SDK's own ``SigningTransport``: the harness never builds
a signature base or signs one itself. The module only turns a keyfile, or a key
the test holds, into the signer an SDK client takes:

- :func:`load_keypair` reads a keyfile's ``(kid, key)`` pair;
- :func:`signing_transport` is the SDK's signer over that pair, with the
  keyid/directory split applied once, here, rather than at every client;
- :func:`unpublished_signer` and :func:`mislabelled_signer` are the signers of the
  "unknown key" and "wrong keyid" negatives, still signing inside the SDK;
- :func:`key_thumbprint` names the key a keyfile holds the way a directory does.

The SDK's transport puts a fresh RFC 9421 ``nonce`` on every signature. Ed25519
is deterministic and the timestamps have one-second resolution, so without a
nonce identical requests signed in the same second share a signature and the
receiver's replay store refuses the second. Uniqueness belongs to the signer, not
to a body field or an advancing expiry. A test that wants a replay sends the same
signed bytes again through ``after_signing`` (``sends=2``, or ``resend`` for a
later send); it never re-signs.
"""

from __future__ import annotations

import json
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat
from fora_sdk.b64 import b64url_decode
from fora_sdk.identity import generate_key
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.thumbprint import thumbprint as ed25519_thumbprint
from fora_sdk.window import Window


def load_keypair(key_path: Path) -> tuple[str, Ed25519PrivateKey]:
    """Read a contributor-style keyfile and return ``(kid, Ed25519PrivateKey)``.

    The key file format is the one written by ``scripts/gen-e2e-keys.sh``:
    ``{"kid": "...", "private_key": "<b64url seed>", "public_key": "..."}``.
    """
    doc = json.loads(key_path.read_text())
    seed = b64url_decode(doc["private_key"])
    return doc["kid"], Ed25519PrivateKey.from_private_bytes(seed)


def signing_transport(
    directory: str, priv: Ed25519PrivateKey, *, window: Window | None = None
) -> SigningTransport:
    """The SDK's request signer over a keyfile's ``(kid, key)`` pair.

    Two different values go in, and the split is load-bearing. After the Web
    Bot Auth split the RFC 9421 ``keyid`` is the key's RFC 7638 thumbprint,
    while the covered ``Signature-Agent`` names the directory the verifier
    fetches to resolve it. The harness's key fixtures carry the DIRECTORY
    under the name ``kid`` — in this stack an identity's directory, its caller
    name and its fixture kid are one string — so passing that as the keyid
    gets the request signed and then refused as an unknown keyid.

    ``window`` mints ``(created, expires)``; ``None`` takes the SDK's own
    clock window at its default validity. The transport supplies a fresh nonce;
    see the module docstring for why no advancing window is needed.
    """
    return SigningTransport(
        window=window,
        signer_seed=priv.private_bytes_raw(),
        keyid=ed25519_thumbprint(priv.public_key().public_bytes_raw()),
        signature_agent=directory_origin(directory),
    )


def directory_origin(directory: str) -> str:
    """The Signature-Agent origin for ``directory``: a bare host becomes its https origin.

    The Web Bot Auth profile names a signer's key directory by its https origin in
    the Signature-Agent member, and every directory in this stack is served over
    https (the capture proxy is its TLS endpoint). The keyfiles name a directory
    by its bare host, so the scheme is added here, once. A value that already
    carries a scheme is passed through unchanged, for the negatives that sign
    under another origin on purpose.
    """
    return directory if "://" in directory else f"https://{directory}"


def unpublished_signer(directory: str) -> SigningTransport:
    """The SDK's request signer naming ``directory``, over a key no directory publishes.

    The key is minted fresh by the SDK's ``fora_sdk.identity.generate_key``, so no
    Web Bot Auth directory can serve it: a verifier that resolves the keyid against
    ``directory`` finds nothing. This is the signer of every "unknown key" negative,
    and it signs inside the SDK like every other signer the harness builds.
    """
    key, _thumbprint = generate_key()
    return signing_transport(directory, key)


def mislabelled_signer(directory: str, priv: Ed25519PrivateKey, keyid: str) -> SigningTransport:
    """The SDK's request signer signing with ``priv`` under a ``keyid`` that is not its thumbprint.

    For the negatives where the keyid names another key the directory publishes, or
    is a free label rather than a thumbprint. A verifier selects the key whose
    thumbprint equals the keyid, so the signature never verifies under it.
    """
    return SigningTransport(
        signer_seed=priv.private_bytes_raw(),
        keyid=keyid,
        signature_agent=directory_origin(directory),
    )


def key_thumbprint(key_path: Path) -> str:
    """Return the RFC 7638 thumbprint of the key stored at ``key_path``.

    The thumbprint is the identity a WBA directory and a revocation list name a
    key by, so a test that has only the keyfile can still ask "which key is this?"
    without reconstructing the JWK by hand.
    """
    _, priv = load_keypair(key_path)
    return ed25519_thumbprint(priv.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw))
