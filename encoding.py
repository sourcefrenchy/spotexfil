"""encoding.py - Payload encoding, encryption, and integrity verification.

Handles the full pipeline:
    File -> gzip compress -> BLAKE2b hash -> AES-256-GCM encrypt
         -> Base64 encode -> JSON wrap

Decoding reverses the pipeline and verifies the integrity hash.
"""

import base64
import gzip
import html
import json
import os
import re
import sys
from hashlib import blake2b

from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC
from cryptography.hazmat.primitives import hashes

__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# AES-256-GCM constants
KEY_SIZE = 32       # 256-bit key
NONCE_SIZE = 12     # 96-bit nonce (recommended for GCM)
SALT_SIZE = 16      # 128-bit salt for PBKDF2
KDF_ITERATIONS = 480_000  # OWASP recommendation for PBKDF2-SHA256

# Compression flag bytes prepended to payload
FLAG_COMPRESSED = b'\x01'
FLAG_RAW = b'\x00'


def derive_key(password: str, salt: bytes) -> bytes:
    """Derive a 256-bit AES key from a password using PBKDF2-SHA256.

    Args:
        password: The encryption password/passphrase.
        salt: Random salt bytes for key derivation.

    Returns:
        32-byte derived key suitable for AES-256.
    """
    kdf = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=KEY_SIZE,
        salt=salt,
        iterations=KDF_ITERATIONS,
    )
    return kdf.derive(password.encode('utf-8'))


def compute_blake2b(data: bytes, digest_size: int = 20) -> str:
    """Compute BLAKE2b hash of data.

    Args:
        data: Bytes to hash.
        digest_size: Digest size in bytes (default 20).

    Returns:
        Hex-encoded hash string.
    """
    h = blake2b(digest_size=digest_size)
    h.update(data)
    return h.hexdigest()


class Subcipher:
    """Encoding, encryption, and decoding operations for payloads.

    Supports two modes:
        - Encrypted (default): AES-256-GCM + Base64 + JSON
        - Legacy plaintext: Base64 + JSON only (for backward compat)

    Compression (gzip) is applied by default before encryption.
    """

    def __init__(self, spot, encryption_key: str = None,
                 compress: bool = True):
        """Initialize Subcipher.

        Args:
            spot: Spot instance for Spotify API access.
            encryption_key: Optional passphrase for AES-256-GCM encryption.
                If None, payloads are Base64-encoded only (legacy mode).
            compress: Whether to gzip-compress payloads (default True).
        """
        self.spotipy = spot
        self.encryption_key = encryption_key
        self.compress = compress

    def _encrypt(self, plaintext: bytes) -> bytes:
        """Encrypt plaintext using AES-256-GCM.

        Format: salt (16) || nonce (12) || ciphertext+tag

        Args:
            plaintext: Data to encrypt.

        Returns:
            Concatenated salt, nonce, and ciphertext bytes.
        """
        salt = os.urandom(SALT_SIZE)
        nonce = os.urandom(NONCE_SIZE)
        key = derive_key(self.encryption_key, salt)
        aesgcm = AESGCM(key)
        ciphertext = aesgcm.encrypt(nonce, plaintext, None)
        return salt + nonce + ciphertext

    def _decrypt(self, data: bytes) -> bytes:
        """Decrypt AES-256-GCM encrypted data.

        Args:
            data: Concatenated salt || nonce || ciphertext+tag.

        Returns:
            Decrypted plaintext bytes.

        Raises:
            ValueError: If data is too short or decryption fails.
        """
        min_length = SALT_SIZE + NONCE_SIZE + 16  # 16 = GCM tag
        if len(data) < min_length:
            raise ValueError(
                f"Encrypted data too short: {len(data)} bytes "
                f"(minimum {min_length})"
            )
        salt = data[:SALT_SIZE]
        nonce = data[SALT_SIZE:SALT_SIZE + NONCE_SIZE]
        ciphertext = data[SALT_SIZE + NONCE_SIZE:]
        key = derive_key(self.encryption_key, salt)
        aesgcm = AESGCM(key)
        return aesgcm.decrypt(nonce, ciphertext, None)

    @staticmethod
    def _compress(data: bytes) -> bytes:
        """Gzip-compress data with flag prefix.

        Returns:
            FLAG_COMPRESSED + gzip'd bytes, or FLAG_RAW + original
            if compression doesn't help.
        """
        compressed = gzip.compress(data, compresslevel=9)
        if len(compressed) < len(data):
            return FLAG_COMPRESSED + compressed
        return FLAG_RAW + data

    @staticmethod
    def _decompress(data: bytes) -> bytes:
        """Decompress data based on flag prefix.

        Args:
            data: Flag byte + payload.

        Returns:
            Decompressed (or raw) bytes.
        """
        if not data:
            return data
        flag = data[:1]
        payload = data[1:]
        if flag == FLAG_COMPRESSED:
            return gzip.decompress(payload)
        return payload

    def encode_payload(self, input_file: str) -> str:
        """Encode a file into a transmittable payload string.

        Pipeline: read -> compress -> BLAKE2b -> encrypt -> Base64 -> JSON

        Args:
            input_file: Path to the file to encode.

        Returns:
            JSON-wrapped Base64 string ready for chunking.

        Raises:
            SystemExit: If the file cannot be read.
        """
        try:
            from pathlib import Path
            plaintext = Path(input_file).read_bytes()
        except OSError as err:
            print(f"[!] Cannot read file: {err}")
            sys.exit(1)

        checksum = compute_blake2b(plaintext)
        print(f"[*] checksum plaintext {checksum}")
        print(f"[*] original size: {len(plaintext)} bytes")

        # Compress
        if self.compress:
            data = self._compress(plaintext)
            saved = len(plaintext) - len(data) + 1  # +1 for flag byte
            if data[0:1] == FLAG_COMPRESSED:
                pct = (saved / len(plaintext) * 100) if plaintext else 0
                print(f"[*] compressed: {len(data)} bytes "
                      f"(saved {pct:.0f}%)")
            else:
                print("[*] compression skipped (no benefit)")
        else:
            data = FLAG_RAW + plaintext
            print("[*] compression disabled")

        if self.encryption_key:
            # Prepend hash for integrity verification after decryption
            hash_bytes = checksum.encode('utf-8')
            # Format: hash_len (2 bytes) || hash || data
            hash_len = len(hash_bytes).to_bytes(2, 'big')
            data_to_encrypt = hash_len + hash_bytes + data
            data = self._encrypt(data_to_encrypt)
            print("[*] payload encrypted with AES-256-GCM")
        else:
            print("[!] WARNING: no encryption key set, payload is plaintext")

        b64data = base64.b64encode(data).decode('utf-8')
        return json.dumps(b64data)

    def decode_payload(self, payload: str) -> bytes:
        """Decode a received payload back to original file bytes.

        Pipeline: unescape -> JSON -> Base64 -> decrypt -> verify -> decompress

        Args:
            payload: The raw concatenated playlist descriptions.

        Returns:
            Original file bytes.

        Raises:
            SystemExit: If decoding or integrity check fails.
        """
        try:
            data = html.unescape(payload)
        except Exception as err:
            print(f"[!] HTML unescape failed: {err}")
            sys.exit(1)

        try:
            b64str = json.loads(data)
        except json.JSONDecodeError as err:
            print(f"[!] JSON decode failed: {err}")
            sys.exit(1)

        b64data = b64str.encode('utf-8')
        raw_bytes = self._decode_base64(b64data)

        if self.encryption_key:
            try:
                decrypted = self._decrypt(raw_bytes)
            except Exception as err:
                print(f"[!] Decryption failed (wrong key?): {err}")
                sys.exit(1)

            # Extract and verify integrity hash
            hash_len = int.from_bytes(decrypted[:2], 'big')
            stored_hash = decrypted[2:2 + hash_len].decode('utf-8')
            compressed_data = decrypted[2 + hash_len:]

            # Decompress
            plaintext = self._decompress(compressed_data)

            actual_hash = compute_blake2b(plaintext)
            if stored_hash != actual_hash:
                print(
                    f"[!] Integrity check FAILED!\n"
                    f"    Expected: {stored_hash}\n"
                    f"    Got:      {actual_hash}"
                )
                sys.exit(1)
            print(f"[*] integrity verified: {actual_hash}")
        else:
            # Legacy mode: decompress then hash
            plaintext = self._decompress(raw_bytes)
            checksum = compute_blake2b(plaintext)
            print(f"[*] checksum payload {checksum} (not verified, no key)")

        return plaintext

    @staticmethod
    def _decode_base64(data: bytes, altchars: bytes = b'+/') -> bytes:
        """Decode Base64 with automatic padding correction.

        Args:
            data: Base64-encoded bytes.
            altchars: Alternative characters for positions 62/63.

        Returns:
            Decoded bytes.
        """
        data = re.sub(rb'[^a-zA-Z0-9%s]+' % altchars, b'', data)
        missing_padding = len(data) % 4
        if missing_padding:
            data += b'=' * (4 - missing_padding)
        return base64.b64decode(data, altchars)
