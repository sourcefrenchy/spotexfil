"""Integration tests - Full send/retrieve cycle with mocked Spotify API.

Simulates the complete exfiltration workflow:
    client encodes -> generates playlists (mocked) -> retrieve -> decode
"""

import os
import tempfile
from unittest.mock import patch

import pytest

from encoding import Subcipher


class FakeSpotifyStore:
    """In-memory Spotify playlist store for integration testing.

    Simulates the Spotify API by storing playlists in a dict,
    allowing full round-trip testing without network calls.
    """

    def __init__(self):
        self.playlists = {}
        self._counter = 0

    def user_playlists(self, username, limit=50, offset=0):
        items = list(self.playlists.values())[offset:offset + limit]
        has_more = offset + limit < len(self.playlists)
        return {
            'items': [
                {'id': p['id'], 'name': p['name']}
                for p in items
            ],
            'next': 'more' if has_more else None,
        }

    def user_playlist_create(self, username, name, public=True,
                             collaborative=False, description=''):
        self._counter += 1
        pid = f'fake_pl_{self._counter}'
        self.playlists[pid] = {
            'id': pid,
            'name': name,
            'description': description,
        }
        return {'id': pid}

    def user_playlist(self, username, playlist_id):
        return self.playlists[playlist_id]

    def user_playlist_unfollow(self, username, playlist_id):
        if playlist_id in self.playlists:
            del self.playlists[playlist_id]

    def user_playlist_add_tracks(self, username, playlist_id, tracks):
        pass  # no-op for testing

    def search(self, q='', type='', limit=1):
        return {
            'artists': {
                'total': 1,
                'items': [{'id': 'artist_fake'}]
            }
        }

    def artist_top_tracks(self, artist_id):
        return {
            'tracks': [{'id': f'track_{i}'} for i in range(5)]
        }


MOCK_ENV = {
    "SPOTIFY_USERNAME": "testuser",
    "SPOTIFY_CLIENT_ID": "fake_id",
    "SPOTIFY_CLIENT_SECRET": "fake_secret",
    "SPOTIFY_REDIRECTURI": "http://localhost:8888/cb",
}


@pytest.fixture
def fake_store():
    """Provide a fresh in-memory Spotify store."""
    return FakeSpotifyStore()


@pytest.fixture
def spot_with_store(fake_store):
    """Create a Spot instance backed by the fake store."""
    with patch.dict(os.environ, MOCK_ENV):
        with patch('spotapi.util.prompt_for_user_token',
                   return_value='fake_token'):
            with patch('spotapi.spotipy.Spotify',
                       return_value=fake_store):
                from spotapi import Spot
                s = Spot()
                return s


class TestFullRoundtrip:
    """End-to-end: encode -> generate_playlists -> retrieve -> decode."""

    def _roundtrip(self, spot, content, encryption_key=None):
        """Run full exfil + retrieval cycle, return decoded bytes."""
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name

        try:
            # Exfiltrate
            cipher_send = Subcipher(spot, encryption_key=encryption_key)
            encoded = cipher_send.encode_payload(filepath)
            spot.clear_data()
            spot.generate_playlists(encoded)

            # Retrieve
            cipher_recv = Subcipher(spot, encryption_key=encryption_key)
            payload = spot.retrieve_playlists()
            decoded = cipher_recv.decode_payload(payload)
            return decoded
        finally:
            os.unlink(filepath)

    def test_text_plaintext_roundtrip(self, spot_with_store):
        """Full roundtrip with text content, no encryption."""
        content = b"Hello, this is a test of SpotExfil!"
        result = self._roundtrip(spot_with_store, content)
        assert result == content

    def test_text_encrypted_roundtrip(self, spot_with_store):
        """Full roundtrip with text content, AES-256-GCM encrypted."""
        content = b"Encrypted secret message for testing."
        result = self._roundtrip(
            spot_with_store, content, encryption_key="s3cret!"
        )
        assert result == content

    def test_binary_encrypted_roundtrip(self, spot_with_store):
        """Full roundtrip with binary content, encrypted."""
        content = os.urandom(512)
        result = self._roundtrip(
            spot_with_store, content, encryption_key="binkey"
        )
        assert result == content

    def test_large_multi_chunk_roundtrip(self, spot_with_store):
        """Full roundtrip forcing multiple playlist chunks."""
        # Create content large enough to need 5+ playlists
        content = b"SpotExfil " * 200  # ~2000 bytes -> ~2700 encoded
        result = self._roundtrip(
            spot_with_store, content, encryption_key="bigkey"
        )
        assert result == content

    def test_empty_file_roundtrip(self, spot_with_store):
        """Full roundtrip with empty file, encrypted."""
        result = self._roundtrip(
            spot_with_store, b"", encryption_key="emptykey"
        )
        assert result == b""

    def test_special_chars_roundtrip(self, spot_with_store):
        """Content with special/unicode characters survives roundtrip."""
        content = "H\u00ebll\u00f6 w\u00f6rld! \u2603 \u2764\ufe0f &amp; <tag>".encode('utf-8')
        result = self._roundtrip(
            spot_with_store, content, encryption_key="unicode!"
        )
        assert result == content

    def test_clear_then_send(self, spot_with_store, fake_store):
        """Old payload is cleared before new one is stored."""
        # Send first payload
        self._roundtrip(spot_with_store, b"first payload")

        # Verify playlists exist
        assert len(fake_store.playlists) > 0

        # Send second payload (clear_data is called inside _roundtrip)
        result = self._roundtrip(spot_with_store, b"second payload")
        assert result == b"second payload"

    def test_wrong_key_retrieval_fails(self, spot_with_store):
        """Retrieval with wrong key fails with SystemExit."""
        content = b"top secret"

        # Encode with one key
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name

        try:
            cipher = Subcipher(spot_with_store, encryption_key="correct")
            encoded = cipher.encode_payload(filepath)
            spot_with_store.generate_playlists(encoded)

            # Try to retrieve with wrong key
            wrong_cipher = Subcipher(spot_with_store, encryption_key="wrong")
            payload = spot_with_store.retrieve_playlists()
            with pytest.raises(SystemExit):
                wrong_cipher.decode_payload(payload)
        finally:
            os.unlink(filepath)


class TestPlaylistOrdering:
    """Verify payload integrity with various playlist orderings."""

    def test_ordering_independent_of_api_order(
            self, spot_with_store, fake_store):
        """Payload decodes correctly regardless of API return order."""
        content = b"X" * 500  # will need multiple chunks

        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(content)
            f.flush()
            filepath = f.name

        try:
            cipher = Subcipher(spot_with_store, encryption_key="ordertest")
            encoded = cipher.encode_payload(filepath)
            spot_with_store.clear_data()
            spot_with_store.generate_playlists(encoded)

            # Scramble playlist order in the store
            items = list(fake_store.playlists.items())
            items.reverse()
            fake_store.playlists = dict(items)

            # Retrieve should still work (sorted by index)
            payload = spot_with_store.retrieve_playlists()
            decoded = cipher.decode_payload(payload)
            assert decoded == content
        finally:
            os.unlink(filepath)
