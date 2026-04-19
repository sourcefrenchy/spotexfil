"""Interoperability tests that validate Python implementations against
shared/test_vectors/ to ensure cross-language compatibility.
"""

import json
from pathlib import Path

import pytest

from spotexfil.crypto import compute_blake2b, derive_key
from spotexfil.protocol import compute_c2_tag, _derive_meta_key

# Load test vectors
_VECTORS_DIR = Path(__file__).resolve().parent.parent.parent / 'shared' / 'test_vectors'


def _load_vectors(name):
    with open(_VECTORS_DIR / name) as f:
        return json.load(f)


class TestBlake2bVectors:
    """Validate BLAKE2b against shared test vectors."""

    @pytest.fixture(autouse=True)
    def load_vectors(self):
        self.vectors = _load_vectors('blake2b_vectors.json')

    def test_all_vectors(self):
        """Each BLAKE2b test vector produces the expected hash."""
        for v in self.vectors:
            input_bytes = bytes.fromhex(v['input'])
            digest_size = v['digest_size']
            expected = v['hash']
            actual = compute_blake2b(input_bytes, digest_size=digest_size)
            assert actual == expected, (
                f"BLAKE2b mismatch for input={v['input']!r}: "
                f"expected={expected}, got={actual}"
            )


class TestKdfVectors:
    """Validate PBKDF2-SHA256 key derivation against shared test vectors."""

    @pytest.fixture(autouse=True)
    def load_vectors(self):
        self.vectors = _load_vectors('kdf_vectors.json')

    def test_all_vectors(self):
        """Each KDF test vector produces the expected key."""
        for v in self.vectors:
            password = v['password']
            salt = bytes.fromhex(v['salt'])
            expected_key = v['key']
            actual_key = derive_key(password, salt).hex()
            assert actual_key == expected_key, (
                f"KDF mismatch for password={password!r}: "
                f"expected={expected_key}, got={actual_key}"
            )


class TestEncryptVectors:
    """Validate AES-256-GCM encryption against shared test vectors."""

    @pytest.fixture(autouse=True)
    def load_vectors(self):
        self.vectors = _load_vectors('encrypt_vectors.json')

    def test_decrypt_vectors(self):
        """Each encrypt test vector decrypts to the expected plaintext."""
        import base64
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM

        for v in self.vectors:
            key = bytes.fromhex(v['key'])
            nonce = bytes.fromhex(v['nonce'])
            ciphertext = base64.b64decode(v['ciphertext'])
            expected_plaintext = v['plaintext']

            aesgcm = AESGCM(key)
            plaintext = aesgcm.decrypt(nonce, ciphertext, None)
            assert plaintext.decode('utf-8') == expected_plaintext, (
                f"Decrypt mismatch for password={v['password']!r}"
            )

    def test_kdf_matches(self):
        """KDF in encrypt vectors produces the expected key."""
        for v in self.vectors:
            password = v['password']
            salt = bytes.fromhex(v['salt'])
            expected_key = v['key']
            actual_key = derive_key(password, salt).hex()
            assert actual_key == expected_key


class TestHmacVectors:
    """Validate HMAC-based key derivation against shared test vectors."""

    @pytest.fixture(autouse=True)
    def load_vectors(self):
        self.vectors = _load_vectors('hmac_vectors.json')

    def test_c2_tag_deterministic(self):
        """C2 tag is deterministic within the same time window."""
        tag1 = compute_c2_tag("test-key")
        tag2 = compute_c2_tag("test-key")
        assert tag1 == tag2
        assert len(tag1) == 12
        # Different keys produce different tags
        tag3 = compute_c2_tag("other-key")
        assert tag1 != tag3

    def test_c2_tags_returns_two(self):
        """compute_c2_tags returns current and previous window."""
        from spotexfil.protocol import compute_c2_tags
        tags = compute_c2_tags("test-key")
        assert len(tags) == 2
        assert len(tags[0]) == 12
        assert len(tags[1]) == 12

    def test_meta_key_vectors(self):
        """HMAC meta key derivation matches shared test vectors."""
        for v in self.vectors:
            key = v['key']
            label = v['label']

            if label == "spotexfil-c2-meta-key":
                actual = _derive_meta_key(key).hex()
                expected = v['full_hex']
                assert actual == expected, (
                    f"Meta key mismatch for key={key!r}: "
                    f"expected={expected}, got={actual}"
                )
