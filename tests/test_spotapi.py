"""Tests for spotapi.py - Spotify API wrapper with mocked API calls."""

import os
from unittest.mock import MagicMock, patch, call

import pytest

# We need to mock environment variables before importing spotapi
MOCK_ENV = {
    "SPOTIFY_USERNAME": "testuser",
    "SPOTIFY_CLIENT_ID": "fake_client_id",
    "SPOTIFY_CLIENT_SECRET": "fake_client_secret",
    "SPOTIFY_REDIRECTURI": "http://localhost:8888/callback",
}


@pytest.fixture
def mock_env():
    """Set up mock environment variables."""
    with patch.dict(os.environ, MOCK_ENV):
        yield


@pytest.fixture
def mock_spotipy():
    """Create a mock spotipy.Spotify instance."""
    mock = MagicMock()
    # Default: user has no playlists
    mock.user_playlists.return_value = {'items': [], 'next': None}
    mock.search.return_value = {
        'artists': {
            'total': 1,
            'items': [{'id': 'artist123'}]
        }
    }
    mock.artist_top_tracks.return_value = {
        'tracks': [
            {'id': f'track{i}'} for i in range(5)
        ]
    }
    return mock


@pytest.fixture
def spot_instance(mock_env, mock_spotipy):
    """Create a Spot instance with mocked auth."""
    with patch('spotapi.util.prompt_for_user_token', return_value='fake_token'):
        with patch('spotapi.spotipy.Spotify', return_value=mock_spotipy):
            from spotapi import Spot
            instance = Spot()
            return instance


# --- Authentication Tests ---

class TestAuthentication:
    def test_missing_env_var_exits(self):
        """Missing environment variables cause sys.exit(1)."""
        from spotapi import Spot
        with patch.dict(os.environ, {}, clear=True):
            with pytest.raises(SystemExit) as exc_info:
                Spot()
            assert exc_info.value.code == 1

    def test_missing_single_var(self, mock_env):
        """Missing a single required var causes exit."""
        from spotapi import Spot
        env = dict(MOCK_ENV)
        del env["SPOTIFY_CLIENT_SECRET"]
        with patch.dict(os.environ, env, clear=True):
            with pytest.raises(SystemExit) as exc_info:
                Spot()
            assert exc_info.value.code == 1

    def test_successful_auth(self, mock_env, mock_spotipy):
        """Successful authentication creates spotipy client."""
        with patch('spotapi.util.prompt_for_user_token', return_value='tok'):
            with patch('spotapi.spotipy.Spotify', return_value=mock_spotipy):
                from spotapi import Spot
                s = Spot()
                assert s.spotipy == mock_spotipy
                assert s.username == "testuser"

    def test_null_token_exits(self, mock_env):
        """Null token causes exit."""
        with patch('spotapi.util.prompt_for_user_token', return_value=None):
            from spotapi import Spot
            with pytest.raises(SystemExit):
                Spot()


# --- Playlist Pagination Tests ---

class TestGetAllPlaylists:
    def test_empty_account(self, spot_instance, mock_spotipy):
        """Empty account returns empty list."""
        mock_spotipy.user_playlists.return_value = {
            'items': [], 'next': None
        }
        result = spot_instance._get_all_playlists()
        assert result == []

    def test_single_page(self, spot_instance, mock_spotipy):
        """Single page of playlists."""
        playlists = [{'id': f'pl{i}', 'name': f'Playlist {i}'}
                     for i in range(10)]
        mock_spotipy.user_playlists.return_value = {
            'items': playlists, 'next': None
        }
        result = spot_instance._get_all_playlists()
        assert len(result) == 10

    def test_multiple_pages(self, spot_instance, mock_spotipy):
        """Multiple pages are fetched via pagination."""
        page1 = [{'id': f'pl{i}', 'name': f'P{i}'} for i in range(50)]
        page2 = [{'id': f'pl{i}', 'name': f'P{i}'} for i in range(50, 75)]

        mock_spotipy.user_playlists.side_effect = [
            {'items': page1, 'next': 'has_more'},
            {'items': page2, 'next': None},
        ]
        result = spot_instance._get_all_playlists()
        assert len(result) == 75


# --- Clear Data Tests ---

class TestClearData:
    def test_clears_payload_playlists(self, spot_instance, mock_spotipy):
        """Only payload playlists are deleted."""
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': '2-payloadChunk'},
                {'id': 'pl3', 'name': 'My Favorites'},
            ],
            'next': None,
        }
        spot_instance.clear_data()

        # Should unfollow only payload playlists
        calls = mock_spotipy.user_playlist_unfollow.call_args_list
        assert len(calls) == 2
        assert calls[0] == call('testuser', 'pl1')
        assert calls[1] == call('testuser', 'pl2')

    def test_clears_nothing_when_empty(self, spot_instance, mock_spotipy):
        """No unfollows when no payload playlists exist."""
        mock_spotipy.user_playlists.return_value = {
            'items': [{'id': 'pl1', 'name': 'My Music'}],
            'next': None,
        }
        spot_instance.clear_data()
        mock_spotipy.user_playlist_unfollow.assert_not_called()


# --- Retrieve Playlists Tests ---

class TestRetrievePlaylists:
    def test_retrieve_ordered_by_index(self, spot_instance, mock_spotipy):
        """Playlists are reassembled in correct order."""
        # Return in wrong API order to verify sorting
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl3', 'name': '3-payloadChunk'},
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': '2-payloadChunk'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.side_effect = [
            {'description': 'chunk1'},
            {'description': 'chunk2'},
            {'description': 'chunk3'},
        ]
        result = spot_instance.retrieve_playlists()
        assert result == 'chunk1chunk2chunk3'

    def test_retrieve_skips_non_payload(self, spot_instance, mock_spotipy):
        """Non-payload playlists are ignored."""
        mock_spotipy.user_playlists.return_value = {
            'items': [
                {'id': 'pl1', 'name': '1-payloadChunk'},
                {'id': 'pl2', 'name': 'My Music'},
            ],
            'next': None,
        }
        mock_spotipy.user_playlist.return_value = {
            'description': 'data'
        }
        result = spot_instance.retrieve_playlists()
        assert result == 'data'
        # user_playlist should only be called for the payload playlist
        mock_spotipy.user_playlist.assert_called_once_with('testuser', 'pl1')

    def test_retrieve_empty(self, spot_instance, mock_spotipy):
        """No payload playlists returns empty string."""
        mock_spotipy.user_playlists.return_value = {
            'items': [], 'next': None
        }
        result = spot_instance.retrieve_playlists()
        assert result == ''

    def test_html_entities_unescaped(self, spot_instance, mock_spotipy):
        """HTML entities in descriptions are unescaped."""
        mock_spotipy.user_playlists.return_value = {
            'items': [{'id': 'pl1', 'name': '1-payloadChunk'}],
            'next': None,
        }
        mock_spotipy.user_playlist.return_value = {
            'description': 'data&amp;more'
        }
        result = spot_instance.retrieve_playlists()
        assert result == 'data&more'


# --- Generate Playlists Tests ---

class TestGeneratePlaylists:
    def test_small_payload_single_playlist(self, spot_instance, mock_spotipy):
        """Small payload creates exactly one playlist."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_instance.generate_playlists("short payload")

        mock_spotipy.user_playlist_create.assert_called_once()
        args = mock_spotipy.user_playlist_create.call_args
        assert args[0][1] == '1-payloadChunk'  # name
        assert args[1]['description'] == 'short payload'

    def test_large_payload_multiple_playlists(
            self, spot_instance, mock_spotipy):
        """Payload > 300 bytes creates multiple playlists."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        payload = "A" * 700  # Should create 3 playlists (300+300+100)
        spot_instance.generate_playlists(payload)

        assert mock_spotipy.user_playlist_create.call_count == 3

    def test_payload_too_large_exits(self, spot_instance):
        """Payload over MAX_PAYLOAD_SIZE causes exit."""
        payload = "X" * 600_000
        with pytest.raises(SystemExit) as exc_info:
            spot_instance.generate_playlists(payload)
        assert exc_info.value.code == 1

    def test_chunks_are_correct_size(self, spot_instance, mock_spotipy):
        """Each chunk except the last is exactly 300 bytes."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        payload = "B" * 650
        spot_instance.generate_playlists(payload)

        calls = mock_spotipy.user_playlist_create.call_args_list
        descriptions = [c[1]['description'] for c in calls]
        assert len(descriptions[0]) == 300
        assert len(descriptions[1]) == 300
        assert len(descriptions[2]) == 50  # remainder

    def test_filler_tracks_added(self, spot_instance, mock_spotipy):
        """Filler tracks are added to each playlist."""
        mock_spotipy.user_playlist_create.return_value = {'id': 'new_pl'}

        spot_instance.generate_playlists("test")

        mock_spotipy.user_playlist_add_tracks.assert_called_once()
        track_ids = mock_spotipy.user_playlist_add_tracks.call_args[0][2]
        assert len(track_ids) == 5


# --- Filler Tracks Tests ---

class TestGetFillerTracks:
    def test_returns_track_ids(self, spot_instance, mock_spotipy):
        """Returns list of track ID strings."""
        tracks = spot_instance._get_filler_tracks()
        assert len(tracks) == 5
        assert all(isinstance(t, str) for t in tracks)

    def test_artist_not_found_returns_empty(
            self, spot_instance, mock_spotipy):
        """Unknown artist returns empty list."""
        mock_spotipy.search.return_value = {
            'artists': {'total': 0, 'items': []}
        }
        tracks = spot_instance._get_filler_tracks()
        assert tracks == []

    def test_custom_count(self, spot_instance, mock_spotipy):
        """Respects custom track count."""
        tracks = spot_instance._get_filler_tracks(count=3)
        assert len(tracks) == 3
