"""Integration tests - Full send/retrieve cycle with mocked Spotify API.

Simulates the complete exfiltration workflow:
    client encodes -> generates playlists (mocked) -> retrieve -> decode
"""

import os
import tempfile
from unittest.mock import patch

import pytest

from spotexfil.crypto import Subcipher
from tests.conftest import MOCK_ENV


@pytest.fixture
def spot_legacy(fake_store):
    """Spot instance with legacy naming."""
    with patch.dict(os.environ, MOCK_ENV):
        with patch('spotexfil.transport.util.prompt_for_user_token',
                   return_value='fake_token'):
            with patch('spotexfil.transport.spotipy.Spotify',
                       return_value=fake_store):
                from spotexfil.transport import Spot
                return Spot(use_cover_names=False)


@pytest.fixture
def spot_cover(fake_store):
    """Spot instance with cover names."""
    with patch.dict(os.environ, MOCK_ENV):
        with patch('spotexfil.transport.util.prompt_for_user_token',
                   return_value='fake_token'):
            with patch('spotexfil.transport.spotipy.Spotify',
                       return_value=fake_store):
                from spotexfil.transport import Spot
                return Spot(use_cover_names=True)


class TestFullRoundtrip:
    """End-to-end: encode -> generate_playlists -> retrieve -> decode."""

    def _roundtrip(self, spot, content, encryption_key=None,
                   compress=True):
        """Run full exfil + retrieval cycle, return decoded bytes."""
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name

        try:
            cipher_send = Subcipher(
                spot, encryption_key=encryption_key, compress=compress
            )
            encoded = cipher_send.encode_payload(filepath)
            spot.clear_data()
            spot.generate_playlists(encoded)

            cipher_recv = Subcipher(
                spot, encryption_key=encryption_key, compress=compress
            )
            payload = spot.retrieve_playlists()
            decoded = cipher_recv.decode_payload(payload)
            return decoded
        finally:
            os.unlink(filepath)

    def test_text_plaintext_roundtrip(self, spot_legacy):
        """Full roundtrip with text, no encryption."""
        content = b"Hello, this is a test of SpotExfil!"
        assert self._roundtrip(spot_legacy, content) == content

    def test_text_encrypted_roundtrip(self, spot_legacy):
        """Full roundtrip with text, AES-256-GCM encrypted."""
        content = b"Encrypted secret message for testing."
        result = self._roundtrip(
            spot_legacy, content, encryption_key="s3cret!"
        )
        assert result == content

    def test_binary_encrypted_roundtrip(self, spot_legacy):
        """Full roundtrip with binary content, encrypted."""
        content = os.urandom(512)
        result = self._roundtrip(
            spot_legacy, content, encryption_key="binkey"
        )
        assert result == content

    def test_large_multi_chunk_roundtrip(self, spot_legacy):
        """Full roundtrip forcing multiple playlist chunks."""
        content = b"SpotExfil " * 200
        result = self._roundtrip(
            spot_legacy, content, encryption_key="bigkey"
        )
        assert result == content

    def test_empty_file_roundtrip(self, spot_legacy):
        """Full roundtrip with empty file, encrypted."""
        result = self._roundtrip(
            spot_legacy, b"", encryption_key="emptykey"
        )
        assert result == b""

    def test_special_chars_roundtrip(self, spot_legacy):
        """Content with special/unicode characters survives roundtrip."""
        content = (
            "H\u00ebll\u00f6 w\u00f6rld! \u2603 \u2764\ufe0f &amp; <tag>"
        ).encode('utf-8')
        result = self._roundtrip(
            spot_legacy, content, encryption_key="unicode!"
        )
        assert result == content

    def test_clear_then_send(self, spot_legacy, fake_store):
        """Old payload is cleared before new one is stored."""
        self._roundtrip(spot_legacy, b"first payload")
        assert len(fake_store.playlists) > 0
        result = self._roundtrip(spot_legacy, b"second payload")
        assert result == b"second payload"

    def test_wrong_key_retrieval_fails(self, spot_legacy):
        """Retrieval with wrong key fails with SystemExit."""
        content = b"top secret"
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name
        try:
            cipher = Subcipher(spot_legacy, encryption_key="correct")
            encoded = cipher.encode_payload(filepath)
            spot_legacy.generate_playlists(encoded)

            wrong_cipher = Subcipher(spot_legacy, encryption_key="wrong")
            payload = spot_legacy.retrieve_playlists()
            with pytest.raises(SystemExit):
                wrong_cipher.decode_payload(payload)
        finally:
            os.unlink(filepath)

    # --- Compression-specific roundtrips ---

    def test_compressed_roundtrip(self, spot_legacy):
        """Compressed payload roundtrips correctly."""
        content = b"Repetitive data for compression. " * 100
        result = self._roundtrip(
            spot_legacy, content, encryption_key="compress",
            compress=True,
        )
        assert result == content

    def test_no_compress_roundtrip(self, spot_legacy):
        """Uncompressed payload roundtrips correctly."""
        content = b"No compression here."
        result = self._roundtrip(
            spot_legacy, content, encryption_key="nocomp",
            compress=False,
        )
        assert result == content

    def test_compressed_saves_playlists(self, spot_legacy, fake_store):
        """Compression reduces number of playlists needed."""
        content = b"AAAA" * 2000  # 8KB, highly compressible

        # With compression
        c1 = Subcipher(spot_legacy, encryption_key="k", compress=True)
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            enc1 = c1.encode_payload(f.name)
        os.unlink(f.name)

        # Without compression
        c2 = Subcipher(spot_legacy, encryption_key="k", compress=False)
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            enc2 = c2.encode_payload(f.name)
        os.unlink(f.name)

        assert len(enc1) < len(enc2)

    # --- Cover names roundtrips ---

    def test_cover_names_roundtrip(self, spot_cover):
        """Full roundtrip with cover names (not legacy naming)."""
        content = b"Cover names test payload data."
        result = self._roundtrip(
            spot_cover, content, encryption_key="cover"
        )
        assert result == content

    def test_cover_names_large_roundtrip(self, spot_cover):
        """Multi-chunk roundtrip with cover names."""
        content = b"Big payload with cover names. " * 100
        result = self._roundtrip(
            spot_cover, content, encryption_key="coverlarge"
        )
        assert result == content

    def test_cover_names_no_payloadchunk_in_names(
            self, spot_cover, fake_store):
        """Cover name mode doesn't expose payloadChunk in names."""
        content = b"stealth test"
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name
        try:
            cipher = Subcipher(spot_cover, encryption_key="stealth")
            encoded = cipher.encode_payload(filepath)
            spot_cover.clear_data()
            spot_cover.generate_playlists(encoded)

            for pl in fake_store.playlists.values():
                assert 'payloadChunk' not in pl['name']
        finally:
            os.unlink(filepath)


class TestPlaylistOrdering:
    """Verify payload integrity with various playlist orderings."""

    def test_ordering_independent_of_api_order(
            self, spot_legacy, fake_store):
        """Payload decodes correctly regardless of API return order."""
        content = b"X" * 500
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name
        try:
            cipher = Subcipher(spot_legacy, encryption_key="ordertest")
            encoded = cipher.encode_payload(filepath)
            spot_legacy.clear_data()
            spot_legacy.generate_playlists(encoded)

            items = list(fake_store.playlists.items())
            items.reverse()
            fake_store.playlists = dict(items)

            payload = spot_legacy.retrieve_playlists()
            decoded = cipher.decode_payload(payload)
            assert decoded == content
        finally:
            os.unlink(filepath)

    def test_cover_names_ordering(self, spot_cover, fake_store):
        """Cover-named playlists reassemble in correct order."""
        content = b"Y" * 1000
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name
        try:
            cipher = Subcipher(spot_cover, encryption_key="coverorder")
            encoded = cipher.encode_payload(filepath)
            spot_cover.clear_data()
            spot_cover.generate_playlists(encoded)

            # Scramble order
            items = list(fake_store.playlists.items())
            items.reverse()
            fake_store.playlists = dict(items)

            payload = spot_cover.retrieve_playlists()
            decoded = cipher.decode_payload(payload)
            assert decoded == content
        finally:
            os.unlink(filepath)
