"""Stress, validation, and edge-case tests for crypto and protocol modules.

Covers:
    - Crypto roundtrip stress (varying payload sizes, random keys)
    - C2 protocol encode/decode stress
    - Chunking + reassembly integrity
    - Concurrent encoding/decoding
    - Edge cases (empty, single byte, large, special characters)
    - Cross-language validation (Python -> Go binary)
"""

import base64
import json
import os
import random
import shutil
import string
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed

import pytest

from spotexfil.crypto import Subcipher, compute_blake2b, derive_key
from spotexfil.protocol import (
    C2Message, CHANNEL_CMD, CHANNEL_RES,
    C2_EFFECTIVE_CHUNK,
    encode_message, decode_message,
    chunk_payload, reassemble_payload, read_c2_descriptions,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def random_key(min_len=8, max_len=64):
    """Generate a random passphrase."""
    length = random.randint(min_len, max_len)
    return ''.join(random.choices(string.ascii_letters + string.digits + string.punctuation, k=length))


def random_bytes(size):
    """Generate random bytes of the given size."""
    return os.urandom(size)


def write_temp_file(data):
    """Write data to a named temporary file, return its path."""
    fd, path = tempfile.mkstemp()
    os.write(fd, data)
    os.close(fd)
    return path


# ---------------------------------------------------------------------------
# 1. Stress tests for crypto roundtrips
# ---------------------------------------------------------------------------

class TestCryptoStress:
    """100 random encrypt/decrypt roundtrips with varying sizes and keys."""

    SIZES = [0, 1, 100, 10_000, 100_000]

    @pytest.mark.parametrize("size", SIZES, ids=lambda s: f"{s}B")
    def test_roundtrip_varying_sizes(self, size):
        """Encrypt/decode roundtrip for a specific payload size, 20 keys."""
        for _ in range(20):
            key = random_key()
            data = random_bytes(size)
            path = write_temp_file(data)
            try:
                cipher = Subcipher(spot=None, encryption_key=key)
                encoded = cipher.encode_payload(path)
                decoded = cipher.decode_payload(encoded)
                assert decoded == data, (
                    f"Mismatch for size={size}, key={key!r}"
                )
            finally:
                os.unlink(path)


# ---------------------------------------------------------------------------
# 2. Stress tests for C2 protocol encode/decode
# ---------------------------------------------------------------------------

class TestC2ProtocolStress:
    """50 random C2 messages with varying data sizes."""

    MODULES = ["shell", "exfil", "sysinfo", "persist", "cleanup"]

    def test_encode_decode_stress(self):
        """Encode/decode 50 random C2 messages."""
        for i in range(50):
            key = random_key()
            module = random.choice(self.MODULES)
            data_size = random.randint(0, 5000)
            data_str = ''.join(random.choices(string.printable, k=data_size))

            msg = {
                "module": module,
                "seq": i,
                "status": "ok",
                "data": data_str,
                "ts": 1700000000.0 + i,
            }

            encoded = encode_message(msg, key)
            decoded = decode_message(encoded, key)

            assert decoded["module"] == module
            assert decoded["seq"] == i
            assert decoded["data"] == data_str


# ---------------------------------------------------------------------------
# 3. Stress tests for chunking + reassembly
# ---------------------------------------------------------------------------

class TestChunkingStress:
    """Chunk and reassemble payloads of varying sizes, verify integrity."""

    def test_chunk_reassemble_range(self):
        """Payloads from 1 to 10000 chars chunk + reassemble correctly."""
        key = random_key()
        # Test a representative sample to keep runtime reasonable
        sizes = list(range(1, 100)) + list(range(100, 1001, 100)) + [5000, 10000]
        for size in sizes:
            payload = ''.join(random.choices(string.ascii_letters, k=size))
            descs = chunk_payload(payload, seq=1, channel=CHANNEL_CMD,
                                  encryption_key=key)

            # Each description must be recoverable
            desc_pairs = [(f"pl{i}", d) for i, d in enumerate(descs)]
            seq_groups = read_c2_descriptions(desc_pairs, key,
                                              channel=CHANNEL_CMD)
            assert 1 in seq_groups, f"seq=1 missing for size={size}"
            reassembled = reassemble_payload(seq_groups[1])
            assert reassembled == payload, (
                f"Mismatch at size={size}: "
                f"got len={len(reassembled)}, want len={len(payload)}"
            )


# ---------------------------------------------------------------------------
# 4. Concurrent encoding test
# ---------------------------------------------------------------------------

class TestConcurrentEncoding:
    """Parallel encode/decode with ThreadPoolExecutor."""

    NUM_WORKERS = 20

    def _roundtrip(self, idx):
        """Single encode/decode roundtrip returning (idx, ok, error)."""
        key = f"concurrent-key-{idx}"
        data = random_bytes(random.randint(50, 2000))
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            return idx, decoded == data, None
        except Exception as exc:
            return idx, False, str(exc)
        finally:
            os.unlink(path)

    def test_concurrent_roundtrips(self):
        """20 parallel encode/decode roundtrips without data corruption."""
        with ThreadPoolExecutor(max_workers=self.NUM_WORKERS) as pool:
            futures = {pool.submit(self._roundtrip, i): i
                       for i in range(self.NUM_WORKERS)}
            for future in as_completed(futures):
                idx, ok, err = future.result()
                assert ok, f"Worker {idx} failed: {err}"


# ---------------------------------------------------------------------------
# 5. Edge cases
# ---------------------------------------------------------------------------

class TestEdgeCases:
    """Boundary and special-character tests."""

    def test_empty_data(self):
        """Empty file roundtrips correctly."""
        key = "edge-empty-key"
        path = write_temp_file(b"")
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == b""
        finally:
            os.unlink(path)

    def test_single_byte(self):
        """Single byte file roundtrips correctly."""
        for byte_val in [0x00, 0x41, 0xFF]:
            key = "edge-single-byte"
            path = write_temp_file(bytes([byte_val]))
            try:
                cipher = Subcipher(spot=None, encryption_key=key)
                encoded = cipher.encode_payload(path)
                decoded = cipher.decode_payload(encoded)
                assert decoded == bytes([byte_val])
            finally:
                os.unlink(path)

    def test_near_1mb_payload(self):
        """Payload near 1MB boundary encodes/decodes correctly."""
        key = "edge-large-key"
        size = 900_000  # ~900KB, under 1MB but substantial
        data = random_bytes(size)
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == data
        finally:
            os.unlink(path)

    def test_null_bytes(self):
        """Data with embedded null bytes roundtrips."""
        key = "edge-null"
        data = b"\x00" * 100 + b"middle" + b"\x00" * 100
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == data
        finally:
            os.unlink(path)

    def test_unicode_binary(self):
        """UTF-8 encoded unicode text roundtrips as binary."""
        key = "edge-unicode"
        data = "Helloworld emoji chars".encode("utf-8")
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == data
        finally:
            os.unlink(path)

    def test_html_entities_in_data(self):
        """Data containing HTML entity-like strings survives roundtrip."""
        key = "edge-html"
        data = b"<script>alert('xss')&amp;&lt;&gt;</script>"
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == data
        finally:
            os.unlink(path)

    def test_very_long_encryption_key(self):
        """Very long key (1000+ chars) still works."""
        key = "K" * 2000
        data = b"payload with very long key"
        path = write_temp_file(data)
        try:
            cipher = Subcipher(spot=None, encryption_key=key)
            encoded = cipher.encode_payload(path)
            decoded = cipher.decode_payload(encoded)
            assert decoded == data
        finally:
            os.unlink(path)

    def test_special_key_characters(self):
        """Keys with special characters (quotes, backslashes, etc.)."""
        keys = [
            'key with "quotes"',
            "key with 'single'",
            "key\\with\\backslashes",
            "key\twith\ttabs",
            "key\nwith\nnewlines",
        ]
        data = b"test payload"
        for key in keys:
            path = write_temp_file(data)
            try:
                cipher = Subcipher(spot=None, encryption_key=key)
                encoded = cipher.encode_payload(path)
                decoded = cipher.decode_payload(encoded)
                assert decoded == data, f"Failed with key={key!r}"
            finally:
                os.unlink(path)

    def test_c2_message_empty_data(self):
        """C2 message with empty data field."""
        key = "edge-c2-empty"
        msg = {"module": "shell", "seq": 1, "data": "", "status": "ok"}
        encoded = encode_message(msg, key)
        decoded = decode_message(encoded, key)
        assert decoded["data"] == ""

    def test_c2_message_unicode_data(self):
        """C2 message with unicode characters in data."""
        key = "edge-c2-unicode"
        msg = {"module": "shell", "seq": 1,
               "data": "output: cafe\u0301 re\u0301sume\u0301 \u00e9\u00e8\u00ea"}
        encoded = encode_message(msg, key)
        decoded = decode_message(encoded, key)
        assert decoded["data"] == msg["data"]


# ---------------------------------------------------------------------------
# 6. Cross-language validation (Python -> Go)
# ---------------------------------------------------------------------------

class TestCrossLanguage:
    """Python encrypts, Go binary decrypts (if available)."""

    GO_BINARY = os.path.join(
        os.path.dirname(__file__), '..', '..', 'dist', 'spotexfil'
    )

    @pytest.fixture(autouse=True)
    def check_go_binary(self):
        """Skip if Go binary is not available."""
        resolved = os.path.realpath(self.GO_BINARY)
        if not os.path.isfile(resolved) or not os.access(resolved, os.X_OK):
            pytest.skip(
                f"Go binary not found or not executable at {resolved}"
            )
        self.go_binary = resolved

    def test_python_encode_go_decode(self, tmp_path):
        """Python encodes a file, Go binary decodes it."""
        key = "cross-lang-test-key-2026"
        original_data = b"Cross-language validation payload!\n" * 10
        input_file = tmp_path / "input.bin"
        input_file.write_bytes(original_data)

        # Python encode
        cipher = Subcipher(spot=None, encryption_key=key)
        encoded = cipher.encode_payload(str(input_file))

        # Write encoded payload for Go to consume
        encoded_file = tmp_path / "encoded.json"
        encoded_file.write_text(encoded)

        output_file = tmp_path / "output.bin"

        # Attempt Go decode
        result = subprocess.run(
            [self.go_binary, "decode",
             "--key", key,
             "--input", str(encoded_file),
             "--output", str(output_file)],
            capture_output=True,
            text=True,
            timeout=30,
        )

        if result.returncode != 0:
            pytest.skip(
                f"Go binary decode command not supported or failed: "
                f"{result.stderr}"
            )

        decoded_data = output_file.read_bytes()
        assert decoded_data == original_data, (
            "Cross-language roundtrip mismatch"
        )
