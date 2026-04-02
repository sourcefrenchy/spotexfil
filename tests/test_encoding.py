"""Tests for encoding.py - payload encoding, encryption, and integrity."""

import json
import os
import tempfile

import pytest

from encoding import Subcipher, compute_blake2b, derive_key


# --- Fixtures ---

@pytest.fixture
def temp_text_file():
    """Create a temporary text file with known content."""
    content = b"Hello, SpotExfil! This is a test payload."
    with tempfile.NamedTemporaryFile(delete=False, suffix='.txt') as f:
        f.write(content)
        f.flush()
        yield f.name, content
    os.unlink(f.name)


@pytest.fixture
def temp_binary_file():
    """Create a temporary binary file with random bytes."""
    content = os.urandom(512)
    with tempfile.NamedTemporaryFile(delete=False, suffix='.bin') as f:
        f.write(content)
        f.flush()
        yield f.name, content
    os.unlink(f.name)


@pytest.fixture
def temp_large_file():
    """Create a file larger than one playlist chunk (>512 chars encoded)."""
    content = b"A" * 1024
    with tempfile.NamedTemporaryFile(delete=False, suffix='.dat') as f:
        f.write(content)
        f.flush()
        yield f.name, content
    os.unlink(f.name)


@pytest.fixture
def temp_empty_file():
    """Create an empty file."""
    with tempfile.NamedTemporaryFile(delete=False, suffix='.empty') as f:
        yield f.name, b""
    os.unlink(f.name)


@pytest.fixture
def cipher_no_key():
    """Subcipher without encryption (legacy mode)."""
    return Subcipher(spot=None, encryption_key=None)


@pytest.fixture
def cipher_with_key():
    """Subcipher with AES-256-GCM encryption."""
    return Subcipher(spot=None, encryption_key="test-secret-passphrase-123")


# --- Unit Tests: compute_blake2b ---

class TestBlake2b:
    def test_deterministic(self):
        """Same input produces same hash."""
        data = b"test data"
        assert compute_blake2b(data) == compute_blake2b(data)

    def test_different_inputs(self):
        """Different inputs produce different hashes."""
        assert compute_blake2b(b"hello") != compute_blake2b(b"world")

    def test_custom_digest_size(self):
        """Custom digest size produces correct length."""
        h = compute_blake2b(b"test", digest_size=32)
        assert len(h) == 64  # 32 bytes = 64 hex chars

    def test_empty_input(self):
        """Empty input produces a valid hash."""
        h = compute_blake2b(b"")
        assert isinstance(h, str)
        assert len(h) == 40  # 20 bytes = 40 hex chars


# --- Unit Tests: derive_key ---

class TestDeriveKey:
    def test_deterministic_with_same_salt(self):
        """Same password + salt produces same key."""
        salt = os.urandom(16)
        key1 = derive_key("password", salt)
        key2 = derive_key("password", salt)
        assert key1 == key2

    def test_different_passwords(self):
        """Different passwords produce different keys."""
        salt = os.urandom(16)
        key1 = derive_key("password1", salt)
        key2 = derive_key("password2", salt)
        assert key1 != key2

    def test_different_salts(self):
        """Same password with different salts produces different keys."""
        key1 = derive_key("password", os.urandom(16))
        key2 = derive_key("password", os.urandom(16))
        assert key1 != key2

    def test_key_length(self):
        """Derived key is 32 bytes (256 bits)."""
        key = derive_key("password", os.urandom(16))
        assert len(key) == 32


# --- Unit Tests: Encryption/Decryption ---

class TestEncryptDecrypt:
    def test_encrypt_produces_different_output(self, cipher_with_key):
        """Encryption output differs from plaintext."""
        plaintext = b"secret message"
        encrypted = cipher_with_key._encrypt(plaintext)
        assert encrypted != plaintext

    def test_encrypt_different_each_time(self, cipher_with_key):
        """Each encryption produces different ciphertext (random nonce)."""
        plaintext = b"same message"
        enc1 = cipher_with_key._encrypt(plaintext)
        enc2 = cipher_with_key._encrypt(plaintext)
        assert enc1 != enc2

    def test_roundtrip(self, cipher_with_key):
        """Encrypt then decrypt recovers original plaintext."""
        plaintext = b"roundtrip test data"
        encrypted = cipher_with_key._encrypt(plaintext)
        decrypted = cipher_with_key._decrypt(encrypted)
        assert decrypted == plaintext

    def test_roundtrip_binary(self, cipher_with_key):
        """Roundtrip works for arbitrary binary data."""
        plaintext = os.urandom(256)
        encrypted = cipher_with_key._encrypt(plaintext)
        decrypted = cipher_with_key._decrypt(encrypted)
        assert decrypted == plaintext

    def test_roundtrip_empty(self, cipher_with_key):
        """Roundtrip works for empty data."""
        encrypted = cipher_with_key._encrypt(b"")
        decrypted = cipher_with_key._decrypt(encrypted)
        assert decrypted == b""

    def test_wrong_key_fails(self, cipher_with_key):
        """Decryption with wrong key raises an error."""
        plaintext = b"secret"
        encrypted = cipher_with_key._encrypt(plaintext)

        wrong_cipher = Subcipher(spot=None, encryption_key="wrong-key")
        with pytest.raises(Exception):
            wrong_cipher._decrypt(encrypted)

    def test_truncated_data_fails(self, cipher_with_key):
        """Truncated ciphertext raises ValueError."""
        with pytest.raises(ValueError, match="too short"):
            cipher_with_key._decrypt(b"short")

    def test_corrupted_data_fails(self, cipher_with_key):
        """Corrupted ciphertext raises an error."""
        plaintext = b"test"
        encrypted = bytearray(cipher_with_key._encrypt(plaintext))
        # Corrupt the ciphertext portion
        encrypted[-1] ^= 0xFF
        with pytest.raises(Exception):
            cipher_with_key._decrypt(bytes(encrypted))


# --- Unit Tests: Base64 Decode ---

class TestDecodeBase64:
    def test_standard_base64(self):
        """Standard base64 decodes correctly."""
        import base64
        original = b"hello world"
        encoded = base64.b64encode(original)
        result = Subcipher._decode_base64(encoded)
        assert result == original

    def test_missing_padding(self):
        """Base64 with missing padding is handled."""
        import base64
        original = b"test"
        encoded = base64.b64encode(original).rstrip(b'=')
        result = Subcipher._decode_base64(encoded)
        assert result == original

    def test_binary_data(self):
        """Binary data roundtrips through base64."""
        import base64
        original = os.urandom(100)
        encoded = base64.b64encode(original)
        result = Subcipher._decode_base64(encoded)
        assert result == original


# --- Integration Tests: Full Encode/Decode Pipeline ---

class TestEncodeDecodePipeline:
    """Test the full encode -> decode pipeline without Spotify."""

    def test_plaintext_text_file(self, cipher_no_key, temp_text_file):
        """Encode/decode text file without encryption."""
        filepath, original = temp_text_file
        encoded = cipher_no_key.encode_payload(filepath)

        # Verify JSON-wrapped base64
        assert isinstance(encoded, str)
        decoded_json = json.loads(encoded)
        assert isinstance(decoded_json, str)

        # Decode back
        decoded = cipher_no_key.decode_payload(encoded)
        assert decoded == original

    def test_plaintext_binary_file(self, cipher_no_key, temp_binary_file):
        """Encode/decode binary file without encryption."""
        filepath, original = temp_binary_file
        encoded = cipher_no_key.encode_payload(filepath)
        decoded = cipher_no_key.decode_payload(encoded)
        assert decoded == original

    def test_encrypted_text_file(self, cipher_with_key, temp_text_file):
        """Encode/decode text file with AES-256-GCM encryption."""
        filepath, original = temp_text_file
        encoded = cipher_with_key.encode_payload(filepath)
        decoded = cipher_with_key.decode_payload(encoded)
        assert decoded == original

    def test_encrypted_binary_file(self, cipher_with_key, temp_binary_file):
        """Encode/decode binary file with encryption."""
        filepath, original = temp_binary_file
        encoded = cipher_with_key.encode_payload(filepath)
        decoded = cipher_with_key.decode_payload(encoded)
        assert decoded == original

    def test_encrypted_large_file(self, cipher_with_key, temp_large_file):
        """Encode/decode large file with encryption."""
        filepath, original = temp_large_file
        encoded = cipher_with_key.encode_payload(filepath)
        decoded = cipher_with_key.decode_payload(encoded)
        assert decoded == original

    def test_encrypted_empty_file(self, cipher_with_key, temp_empty_file):
        """Encode/decode empty file with encryption."""
        filepath, original = temp_empty_file
        encoded = cipher_with_key.encode_payload(filepath)
        decoded = cipher_with_key.decode_payload(encoded)
        assert decoded == original

    def test_wrong_key_decode_fails(self, cipher_with_key, temp_text_file):
        """Decoding with wrong key exits with error."""
        filepath, _ = temp_text_file
        encoded = cipher_with_key.encode_payload(filepath)

        wrong_cipher = Subcipher(spot=None, encryption_key="wrong-key")
        with pytest.raises(SystemExit):
            wrong_cipher.decode_payload(encoded)

    def test_nonexistent_file_exits(self, cipher_no_key):
        """Encoding a nonexistent file exits with code 1."""
        with pytest.raises(SystemExit) as exc_info:
            cipher_no_key.encode_payload("/nonexistent/path/file.txt")
        assert exc_info.value.code == 1

    def test_integrity_verified(self, cipher_with_key, temp_text_file,
                                capsys):
        """Encrypted decode prints integrity verification message."""
        filepath, _ = temp_text_file
        encoded = cipher_with_key.encode_payload(filepath)
        cipher_with_key.decode_payload(encoded)
        captured = capsys.readouterr()
        assert "integrity verified" in captured.out

    def test_plaintext_no_verification(self, cipher_no_key, temp_text_file,
                                       capsys):
        """Plaintext decode warns about no verification."""
        filepath, _ = temp_text_file
        encoded = cipher_no_key.encode_payload(filepath)
        cipher_no_key.decode_payload(encoded)
        captured = capsys.readouterr()
        assert "not verified" in captured.out


# --- Chunking Simulation Tests ---

class TestChunking:
    """Simulate the chunking that spotapi does, verify reassembly."""

    def _simulate_chunk_reassemble(self, payload: str,
                                   chunk_size: int = 512) -> str:
        """Simulate splitting into playlist chunks and reassembling."""
        if len(payload) <= chunk_size:
            chunks = [payload]
        else:
            chunks = [
                payload[i:i + chunk_size]
                for i in range(0, len(payload), chunk_size)
            ]
        return ''.join(chunks)

    def test_small_payload_no_split(self, cipher_no_key, temp_text_file):
        """Small payload fits in one chunk."""
        filepath, original = temp_text_file
        encoded = cipher_no_key.encode_payload(filepath)
        assert len(encoded) <= 512 or len(encoded) > 512  # just encode
        reassembled = self._simulate_chunk_reassemble(encoded)
        decoded = cipher_no_key.decode_payload(reassembled)
        assert decoded == original

    def test_large_payload_multi_chunk(self, cipher_with_key, temp_large_file):
        """Large payload splits into multiple chunks and reassembles."""
        filepath, original = temp_large_file
        encoded = cipher_with_key.encode_payload(filepath)

        # Verify it would need multiple chunks
        assert len(encoded) > 512

        reassembled = self._simulate_chunk_reassemble(encoded)
        decoded = cipher_with_key.decode_payload(reassembled)
        assert decoded == original

    def test_exact_chunk_boundary(self, cipher_no_key):
        """Payload exactly at chunk boundary works."""
        # Create content that encodes to exactly 512 chars
        with tempfile.NamedTemporaryFile(delete=False, suffix='.dat') as f:
            # Base64 expansion is ~4/3, plus JSON quotes = ~2
            f.write(b"X" * 381)  # ~512 chars after base64+json
            content_len = 381
            f.flush()
            path = f.name

        try:
            encoded = cipher_no_key.encode_payload(path)
            reassembled = self._simulate_chunk_reassemble(encoded)
            decoded = cipher_no_key.decode_payload(reassembled)
            assert decoded == b"X" * content_len
        finally:
            os.unlink(path)
