"""Shared test fixtures for SpotExfil tests."""

import os
import sys

import pytest

# Ensure the package is importable
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))


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
        pass

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
